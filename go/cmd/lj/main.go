// Command lj is a small LINEARJEPH command line for CI: whoami, shield gate and fix run --wait. The full lj
// (tables, login, every product) ships with the Node and Python SDKs.
//
//	lj whoami [--json]
//	lj shield gate --project <id> [--fail-on high] [--timeout 20m] [--json]
//	lj fix run --project <id> --issue "<text>" [--wait] [--timeout 30m] [--json]
//
// The key comes from --key, then LJ_API_KEY, then ~/.config/linearjeph/credentials. LJ_BASE_URL overrides the
// server. Exit codes: 0 ok, 1 gate failed or job failed, 2 usage or auth error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/kingtechies/linearjeph-sdk/go/products"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

const help = `lj: LINEARJEPH command line (CI subset)

usage:
  lj whoami [--json]
  lj shield gate --project <id> [--fail-on low|medium|high|critical] [--timeout 20m] [--json]
  lj fix run --project <id> --issue "<text>" [--wait] [--timeout 30m] [--json]

global flags (before the command or after it): --key <api key>, --base-url <url>
key lookup: --key, then LJ_API_KEY, then ~/.config/linearjeph/credentials
exit codes: 0 ok, 1 gate failed or job failed, 2 usage or auth error
`

type env struct {
	out, err io.Writer
}

func run(args []string, out, errw io.Writer) int {
	e := env{out, errw}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(out, help)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch args[0] {
	case "whoami":
		return e.whoami(ctx, args[1:])
	case "shield":
		if len(args) > 1 && args[1] == "gate" {
			return e.shieldGate(ctx, args[2:])
		}
	case "fix":
		if len(args) > 1 && args[1] == "run" {
			return e.fixRun(ctx, args[2:])
		}
	}
	fmt.Fprintf(errw, "lj: unknown command %q (this build has whoami, shield gate, fix run)\n", strings.Join(args, " "))
	return 2
}

type common struct {
	key, base string
	asJSON    bool
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.key, "key", "", "API key")
	fs.StringVar(&c.base, "base-url", "", "server URL")
	fs.BoolVar(&c.asJSON, "json", false, "raw JSON output")
}

func parse(fs *flag.FlagSet, args []string, errw io.Writer) bool {
	fs.SetOutput(errw)
	return fs.Parse(args) == nil
}

func credentialsKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".config", "linearjeph", "credentials"))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(b))
	var j struct {
		APIKey string `json:"api_key"`
		Key    string `json:"key"`
	}
	if json.Unmarshal([]byte(text), &j) == nil && (j.APIKey != "" || j.Key != "") {
		if j.APIKey != "" {
			return j.APIKey
		}
		return j.Key
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "="); i > 0 && (strings.TrimSpace(line[:i]) == "api_key" || strings.TrimSpace(line[:i]) == "key") {
			return strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		}
		if strings.HasPrefix(line, "ljk_") {
			return line
		}
	}
	return ""
}

func (e env) client(c common) (*products.Client, int) {
	key := c.key
	if key == "" {
		key = os.Getenv("LJ_API_KEY")
	}
	if key == "" {
		key = credentialsKey()
	}
	if key == "" {
		fmt.Fprintln(e.err, "lj: no API key. Use --key, LJ_API_KEY or `lj login` from the Node or Python CLI.")
		return nil, 2
	}
	poll, _ := time.ParseDuration(os.Getenv("LJ_POLL_INTERVAL")) // optional, default 3s
	cl, err := products.New(products.Options{APIKey: key, BaseURL: c.base, PollInterval: poll})
	if err != nil {
		fmt.Fprintln(e.err, "lj:", err)
		return nil, 2
	}
	return cl, 0
}

// fail maps an error to an exit code: auth problems are 2, everything else 1.
func (e env) fail(err error) int {
	var ae *products.ApiError
	if errors.As(err, &ae) {
		fmt.Fprintf(e.err, "lj: %s (%d %s)\n", ae.Message, ae.Status, ae.Code)
		if ae.Status == 401 || ae.Status == 403 {
			return 2
		}
		return 1
	}
	fmt.Fprintln(e.err, "lj:", err)
	return 1
}

func (e env) printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(e.out, string(b))
}

func (e env) whoami(ctx context.Context, args []string) int {
	var c common
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	c.bind(fs)
	if !parse(fs, args, e.err) {
		return 2
	}
	cl, code := e.client(c)
	if cl == nil {
		return code
	}
	who, err := cl.Whoami(ctx)
	if err != nil {
		return e.fail(err)
	}
	if c.asJSON {
		e.printJSON(who)
		return 0
	}
	org, _ := who["organization"].(map[string]any)
	key, _ := who["key"].(map[string]any)
	fmt.Fprintf(e.out, "organization  %s (%s)\n", products.Str(org, "name"), products.Str(org, "id"))
	fmt.Fprintf(e.out, "key           %s (%s_...)\n", products.Str(key, "name"), "ljk_"+products.Str(key, "prefix"))
	scopes, _ := who["scopes"].([]any)
	var names []string
	for _, s := range scopes {
		names = append(names, fmt.Sprint(s))
	}
	fmt.Fprintf(e.out, "scopes        %s\n", strings.Join(names, ", "))
	return 0
}

func (e env) shieldGate(ctx context.Context, args []string) int {
	var c common
	var project int
	var failOn string
	var timeout time.Duration
	fs := flag.NewFlagSet("shield gate", flag.ContinueOnError)
	c.bind(fs)
	fs.IntVar(&project, "project", 0, "vibe project id")
	fs.StringVar(&failOn, "fail-on", "high", "lowest severity that fails the gate")
	fs.DurationVar(&timeout, "timeout", 20*time.Minute, "how long to wait for the scan")
	if !parse(fs, args, e.err) {
		return 2
	}
	if project <= 0 {
		fmt.Fprintln(e.err, "lj shield gate: --project <id> is required")
		return 2
	}
	if products.SeverityRank(failOn) < 1 {
		fmt.Fprintln(e.err, "lj shield gate: --fail-on must be low, medium, high or critical")
		return 2
	}
	cl, code := e.client(c)
	if cl == nil {
		return code
	}
	started, err := cl.Shield.VibeScan(ctx, project)
	if err != nil {
		return e.fail(err)
	}
	scan, err := cl.Shield.WaitVibeScan(ctx, products.Int(started, "id"), timeout)
	if err != nil {
		return e.fail(err)
	}
	if products.Str(scan, "status") == "failed" {
		fmt.Fprintf(e.err, "lj: scan failed: %s\n", products.Str(scan, "error"))
		return 1
	}
	hits, err := cl.Shield.Gate(scan, failOn)
	if err != nil {
		return e.fail(err)
	}
	if c.asJSON {
		e.printJSON(products.JSON{"scan": scan, "fail_on": failOn, "blocking": hits, "passed": len(hits) == 0})
	} else {
		fmt.Fprintf(e.out, "scan %d: grade %s, score %s\n", products.Int(scan, "id"), products.Str(scan, "grade"), products.Str(scan, "score"))
		for _, f := range hits {
			fmt.Fprintf(e.out, "  %-8s %s (%s)\n", strings.ToUpper(products.Str(f, "severity")), products.Str(f, "title"), products.Str(f, "location"))
		}
		if len(hits) == 0 {
			fmt.Fprintf(e.out, "gate passed: no findings at or above %s\n", failOn)
		} else {
			fmt.Fprintf(e.out, "gate failed: %d finding(s) at or above %s\n", len(hits), failOn)
		}
	}
	if len(hits) > 0 {
		return 1
	}
	return 0
}

func (e env) fixRun(ctx context.Context, args []string) int {
	var c common
	var project int
	var issue string
	var wait bool
	var timeout time.Duration
	fs := flag.NewFlagSet("fix run", flag.ContinueOnError)
	c.bind(fs)
	fs.IntVar(&project, "project", 0, "fix project id")
	fs.StringVar(&issue, "issue", "", "describe the problem")
	fs.BoolVar(&wait, "wait", false, "wait for the job to finish")
	fs.DurationVar(&timeout, "timeout", 30*time.Minute, "how long to wait")
	if !parse(fs, args, e.err) {
		return 2
	}
	if project <= 0 || strings.TrimSpace(issue) == "" {
		fmt.Fprintln(e.err, "lj fix run: --project <id> and --issue \"<text>\" are required")
		return 2
	}
	cl, code := e.client(c)
	if cl == nil {
		return code
	}
	p, err := cl.Fix.Project(ctx, project)
	if err != nil {
		return e.fail(err)
	}
	job, err := cl.Fix.CreateJob(ctx, products.JSON{"source_id": products.Str(p, "source_id"), "issue": issue})
	if err != nil {
		return e.fail(err)
	}
	if wait {
		job, err = cl.Fix.Wait(ctx, products.Int(job, "id"), timeout)
		if err != nil {
			return e.fail(err)
		}
	}
	if c.asJSON {
		e.printJSON(job)
	} else {
		fmt.Fprintf(e.out, "job %d: %s\n", products.Int(job, "id"), products.Str(job, "status_label"))
		if u := products.Str(job, "pr_url"); u != "" {
			fmt.Fprintf(e.out, "pull request: %s\n", u)
		}
		if m := products.Str(job, "error"); m != "" {
			fmt.Fprintf(e.out, "error: %s\n", m)
		}
	}
	if products.FailedJobStatuses[products.Str(job, "status")] {
		return 1
	}
	return 0
}
