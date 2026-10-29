// Package linearjeph is the LINEARJEPH error tracking, performance, heartbeat and dependency-reporting SDK.
// Standard library only. Every exported function is safe to call from any goroutine and never panics into the
// caller; delivery happens on a background goroutine with a bounded queue (call Flush before exit).
package linearjeph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Version is the SDK version sent in the User-Agent header.
const Version = "1.0.0"

// DefaultEndpoint is used when Options.Endpoint is empty.
const DefaultEndpoint = "https://linearjeph.cc"

const (
	eventsPath    = "/ingest/v1/events"
	heartbeatPath = "/ingest/v1/heartbeat"
	packagesPath  = "/ingest/v1/packages"
	maxFrames     = 50
	maxTags       = 20
	batchBytes    = 200 * 1024
	filtered      = "[Filtered]"
	selfPrefix    = "github.com/kingtechies/linearjeph-sdk/go."
)

// Event is one ingest event (see docs/sdk/ingest-api.md). BeforeSend receives and may modify it.
type Event map[string]any

// Options configures Init. Zero values select the documented defaults.
type Options struct {
	Key         string            // application ingest key (required)
	Endpoint    string            // default https://linearjeph.cc
	Release     string            // e.g. "1.4.2" or a git SHA
	Environment string            // default "production"
	SampleRate  float64           // fraction of errors sent, (0,1]; 0 means 1 (send all)
	TracesRate  float64           // fraction of transactions sent, (0,1]; 0 means 1
	BeforeSend  func(Event) Event // modify an event or return nil to drop it; a panic drops the event
	ScrubKeys   []string          // extra tag-key substrings to filter (case-insensitive)
	QueueSize   int               // bounded queue length; default 100 (full queue drops)
	BatchSize   int               // max events per request (1..100); default 20
	Retries     int               // retries after the first attempt; default 4 (negative = none)
	Backoff     time.Duration     // base backoff, doubles per attempt with jitter; default 500ms
	Timeout     time.Duration     // per-request timeout; default 10s
	Debug       bool              // log SDK problems with the standard logger
	HTTPClient  *http.Client      // optional custom client (proxy, TLS); Timeout still applies per request
}

type job struct {
	path string
	body any
}

type sdk struct {
	opts    Options
	queue   chan job
	pending atomic.Int64
	dropped atomic.Int64
	off     atomic.Bool // 401: key rejected
	closed  atomic.Bool
	client  *http.Client
	done    chan struct{}
}

var (
	mu    sync.RWMutex
	cur   *sdk
	scope = struct {
		sync.Mutex
		tags map[string]string
		user string
	}{tags: map[string]string{}}
	hbName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	pkName = regexp.MustCompile(`^[\w@./+~:-]{1,200}$`)
	pkVer  = regexp.MustCompile(`^[\w.+~!:-]{1,100}$`)
	secKey = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|authorization|auth|cookie|api[_-]?key|apikey|x-lj-key|private[_-]?key|credential|session|csrf|bearer)`)
	secTxt = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|apikey|authorization|access[_-]?key)\b(\s*[=:]\s*)(?:bearer\s+)?[^\s,;&'"]+`)
	bearer = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
)

func get() *sdk {
	mu.RLock()
	defer mu.RUnlock()
	return cur
}

func dbg(s *sdk, format string, a ...any) {
	if s != nil && s.opts.Debug {
		fmt.Fprintf(os.Stderr, "[linearjeph] "+format+"\n", a...)
	}
}

// Init configures the SDK and starts the background sender. It can be called again (the last call wins; the previous
// queue is flushed best-effort). It returns an error only for a missing key; the SDK then stays disabled.
func Init(o Options) error {
	if strings.TrimSpace(o.Key) == "" {
		return errors.New("linearjeph: Options.Key is required")
	}
	if o.Endpoint == "" {
		o.Endpoint = DefaultEndpoint
	}
	o.Endpoint = strings.TrimRight(o.Endpoint, "/")
	if o.Environment == "" {
		o.Environment = "production"
	}
	if o.SampleRate <= 0 || o.SampleRate > 1 {
		o.SampleRate = 1
	}
	if o.TracesRate <= 0 || o.TracesRate > 1 {
		o.TracesRate = 1
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 100
	}
	if o.BatchSize <= 0 || o.BatchSize > 100 {
		o.BatchSize = 20
	}
	if o.Retries == 0 {
		o.Retries = 4
	} else if o.Retries < 0 {
		o.Retries = 0
	}
	if o.Backoff <= 0 {
		o.Backoff = 500 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	s := &sdk{opts: o, queue: make(chan job, o.QueueSize), done: make(chan struct{})}
	s.client = o.HTTPClient
	if s.client == nil {
		s.client = &http.Client{}
	}
	mu.Lock()
	old := cur
	cur = s
	mu.Unlock()
	if old != nil {
		old.closed.Store(true)
		close(old.queue)
	}
	go s.worker()
	return nil
}

// Close flushes (up to timeout) and stops the sender.
func Close(timeout time.Duration) bool {
	ok := Flush(timeout)
	mu.Lock()
	s := cur
	cur = nil
	mu.Unlock()
	if s != nil {
		s.closed.Store(true)
		close(s.queue)
	}
	return ok
}

// Flush waits up to timeout for queued items to be delivered. It returns true when the queue is empty.
func Flush(timeout time.Duration) bool {
	s := get()
	if s == nil {
		return true
	}
	end := time.Now().Add(timeout)
	for s.pending.Load() > 0 && !s.off.Load() {
		if time.Now().After(end) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// Dropped returns how many items were discarded because the queue was full.
func Dropped() int64 {
	if s := get(); s != nil {
		return s.dropped.Load()
	}
	return 0
}

// SetTag attaches a tag to every later event (max 20 tags; key 50 chars, value 200).
func SetTag(key, value string) {
	scope.Lock()
	scope.tags[key] = value
	scope.Unlock()
}

// SetUser attaches a user id to later events. An empty string clears it.
func SetUser(id string) {
	scope.Lock()
	scope.user = id
	scope.Unlock()
}

// ---------------------------------------------------------------- scrubbing

func sensitive(s *sdk, key string) bool {
	if secKey.MatchString(key) {
		return true
	}
	if s != nil {
		lk := strings.ToLower(key)
		for _, x := range s.opts.ScrubKeys {
			if x != "" && strings.Contains(lk, strings.ToLower(x)) {
				return true
			}
		}
	}
	return false
}

func scrubText(v string) string {
	v = secTxt.ReplaceAllString(v, "${1}${2}"+filtered)
	return bearer.ReplaceAllString(v, "Bearer "+filtered)
}

// Scrub returns a deep copy of maps/slices/strings with values under sensitive keys (password, secret, token,
// authorization, cookie, api_key, ...) replaced by "[Filtered]". Use it before putting your own data in tags.
func Scrub(v any) any { return scrub(get(), v, 0) }

func scrub(s *sdk, v any, depth int) any {
	if depth > 8 {
		return filtered
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if sensitive(s, k) {
				out[k] = filtered
			} else {
				out[k] = scrub(s, val, depth+1)
			}
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, val := range x {
			if sensitive(s, k) {
				out[k] = filtered
			} else {
				out[k] = scrubText(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = scrub(s, val, depth+1)
		}
		return out
	case string:
		return scrubText(x)
	}
	return v
}

func clip(v string, n int) string {
	if len(v) <= n {
		return v
	}
	for n > 0 && n < len(v) && v[n]&0xC0 == 0x80 { // do not cut a UTF-8 sequence
		n--
	}
	return v[:n]
}

func cleanTags(s *sdk, in map[string]string) map[string]string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]string{}
	for _, k := range keys {
		if len(out) >= maxTags {
			break
		}
		if sensitive(s, k) {
			out[clip(k, 50)] = filtered
		} else {
			out[clip(k, 50)] = clip(scrubText(in[k]), 200)
		}
	}
	return out
}

// ---------------------------------------------------------------- events

type frame struct {
	Filename string `json:"filename"`
	Function string `json:"function"`
	Lineno   int    `json:"lineno"`
	InApp    bool   `json:"in_app"`
}

func stack(skip int) []frame {
	pcs := make([]uintptr, 128)
	n := runtime.Callers(skip, pcs)
	fr := runtime.CallersFrames(pcs[:n])
	goroot := runtime.GOROOT()
	var out []frame
	for {
		f, more := fr.Next()
		if f.Function != "" && !strings.HasPrefix(f.Function, "runtime.") {
			in := !strings.HasPrefix(f.Function, selfPrefix) && !strings.Contains(f.File, "/pkg/mod/") &&
				(goroot == "" || !strings.HasPrefix(f.File, goroot)) && !strings.HasPrefix(f.Function, "net/http.")
			out = append(out, frame{f.File, f.Function, f.Line, in})
		}
		if !more {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // oldest first, as the ingest expects
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > maxFrames {
		out = out[len(out)-maxFrames:]
	}
	return out
}

func base(s *sdk, typ string) Event {
	return Event{"type": typ, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "release": s.opts.Release,
		"environment": s.opts.Environment, "tags": map[string]string{}, "user": map[string]string{}}
}

func errorEvent(s *sdk, typ, value string, st []frame) Event {
	e := base(s, "error")
	if st == nil {
		st = []frame{}
	}
	e["exception"] = map[string]any{"type": clip(typ, 200), "value": clip(scrubText(value), 500), "stacktrace": st}
	return e
}

// emit applies sampling, scope tags, scrubbing and BeforeSend, then queues. rate is the sample fraction.
func emit(s *sdk, e Event, tags map[string]string, rate float64) {
	if s == nil {
		return
	}
	if rate < 1 && rand.Float64() >= rate {
		return
	}
	merged := map[string]string{}
	scope.Lock()
	for k, v := range scope.tags {
		merged[k] = v
	}
	user := scope.user
	scope.Unlock()
	for k, v := range tags {
		merged[k] = v
	}
	if t, ok := e["tags"].(map[string]string); ok {
		for k, v := range t {
			merged[k] = v
		}
	}
	e["tags"] = cleanTags(s, merged)
	if user != "" {
		e["user"] = map[string]string{"id": clip(user, 100)}
	}
	if s.opts.BeforeSend != nil {
		kept := func() (out Event) {
			defer func() {
				if recover() != nil {
					dbg(s, "BeforeSend panicked; event dropped")
					out = nil
				}
			}()
			return s.opts.BeforeSend(e)
		}()
		if kept == nil {
			return
		}
		e = kept
		if t, ok := e["tags"].(map[string]string); ok {
			e["tags"] = cleanTags(s, t)
		}
	}
	s.enqueue(eventsPath, e)
}

// CaptureError reports an error with the stack at the call site. nil is ignored.
func CaptureError(err error) { CaptureErrorTags(err, nil) }

// CaptureErrorTags is CaptureError with extra tags for this event.
func CaptureErrorTags(err error, tags map[string]string) {
	defer func() { _ = recover() }()
	if err == nil {
		return
	}
	s := get()
	if s == nil {
		return
	}
	emit(s, errorEvent(s, fmt.Sprintf("%T", err), err.Error(), stack(3)), tags, s.opts.SampleRate)
}

// Capture reports an error, a string or any value (fmt.Sprint). Handy with recover(): linearjeph.Capture(recover()).
func Capture(v any) {
	defer func() { _ = recover() }()
	switch x := v.(type) {
	case nil:
		return
	case error:
		CaptureError(x)
	default:
		s := get()
		if s == nil {
			return
		}
		emit(s, errorEvent(s, fmt.Sprintf("%T", v), fmt.Sprint(v), stack(3)), nil, s.opts.SampleRate)
	}
}

// CaptureMessage reports a message. The ingest stores it as an error event of type "Message".
func CaptureMessage(msg string) {
	defer func() { _ = recover() }()
	if s := get(); s != nil {
		emit(s, errorEvent(s, "Message", msg, nil), nil, s.opts.SampleRate)
	}
}

// CaptureTransaction reports one timed operation. status is "ok" or "error" (any short string is accepted).
func CaptureTransaction(name string, d time.Duration, status string) {
	defer func() { _ = recover() }()
	s := get()
	if s == nil || name == "" || d < 0 {
		return
	}
	if status == "" {
		status = "ok"
	}
	e := base(s, "transaction")
	e["transaction"] = map[string]any{"name": clip(name, 200), "duration_ms": float64(d) / float64(time.Millisecond), "status": clip(status, 20)}
	emit(s, e, nil, s.opts.TracesRate)
}

// Txn is a running transaction; call End once.
type Txn struct {
	name  string
	start time.Time
	done  atomic.Bool
}

// StartTransaction begins timing. `defer linearjeph.StartTransaction("job").End("ok")` evaluates End's argument at once;
// prefer `t := StartTransaction(..); defer t.End("ok")` or t.EndErr(err).
func StartTransaction(name string) *Txn { return &Txn{name: name, start: time.Now()} }

// End records the transaction (once).
func (t *Txn) End(status string) {
	if t != nil && t.done.CompareAndSwap(false, true) {
		CaptureTransaction(t.name, time.Since(t.start), status)
	}
}

// EndErr records the transaction with status "ok" or "error" depending on err.
func (t *Txn) EndErr(err error) {
	if err != nil {
		t.End("error")
		return
	}
	t.End("ok")
}

// Heartbeat checks in for a cron job or worker. status is "ok" or "error"; intervalS (30..2592000, 0 = unset) is how
// often the monitor should expect a check-in. Invalid names (allowed: A-Z a-z 0-9 . _ -, max 64) are ignored.
func Heartbeat(name, status string, intervalS int) {
	defer func() { _ = recover() }()
	s := get()
	if s == nil {
		return
	}
	if !hbName.MatchString(name) {
		dbg(s, "heartbeat name %q is invalid; ignored", name)
		return
	}
	if status != "ok" {
		status = "error"
	}
	b := map[string]any{"name": name, "status": status}
	if intervalS > 0 {
		if intervalS < 30 {
			intervalS = 30
		}
		if intervalS > 30*86400 {
			intervalS = 30 * 86400
		}
		b["interval_s"] = intervalS
	}
	s.enqueue(heartbeatPath, b)
}

// Monitor runs fn and sends a heartbeat: "ok" when it returns nil, "error" otherwise (the error is returned
// unchanged). A panic in fn is reported as an error heartbeat and re-panicked. Flush before the process exits.
func Monitor(name string, intervalS int, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			Heartbeat(name, "error", intervalS)
			panic(p)
		}
		if err != nil {
			Heartbeat(name, "error", intervalS)
		} else {
			Heartbeat(name, "ok", intervalS)
		}
	}()
	return fn()
}

// ---------------------------------------------------------------- packages

type pkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func sendPackages(list []pkg) int {
	s := get()
	if s == nil {
		return 0
	}
	seen := map[pkg]bool{}
	out := []pkg{}
	size := 64
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for _, p := range list {
		if !pkName.MatchString(p.Name) || !pkVer.MatchString(p.Version) || seen[p] {
			continue
		}
		seen[p] = true
		size += len(p.Name) + len(p.Version) + 30
		if len(out) >= 5000 || size > batchBytes { // one request holds the whole list (server replaces per request)
			break
		}
		out = append(out, p)
	}
	s.enqueue(packagesPath, map[string]any{"ecosystem": "Go", "packages": out})
	return len(out)
}

// ReportPackages sends the module dependencies compiled into the running binary (runtime/debug.ReadBuildInfo) to the
// dependency audit. Returns the number queued (0 when build info is unavailable, e.g. `go run` of a single file).
func ReportPackages() int {
	defer func() { _ = recover() }()
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return 0
	}
	var list []pkg
	for _, d := range bi.Deps {
		m := d
		if m.Replace != nil && m.Replace.Version != "" {
			m = m.Replace
		}
		if m.Version != "" && m.Version != "(devel)" {
			list = append(list, pkg{m.Path, m.Version})
		}
	}
	return sendPackages(list)
}

// ReportGoSum parses a go.sum file (module versions, ignoring the "/go.mod" hash lines) and sends it.
func ReportGoSum(path string) (n int, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("linearjeph: %v", p)
		}
	}()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var list []pkg
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && !strings.HasSuffix(f[1], "/go.mod") {
			list = append(list, pkg{f[0], f[1]})
		}
	}
	return sendPackages(list), nil
}

// ReportGoMod parses the require directives of a go.mod file and sends them.
func ReportGoMod(path string) (n int, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("linearjeph: %v", p)
		}
	}()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var list []pkg
	inBlock := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
		switch {
		case line == "require (":
			inBlock = true
		case line == ")":
			inBlock = false
		case strings.HasPrefix(line, "require "):
			if f := strings.Fields(strings.TrimPrefix(line, "require ")); len(f) >= 2 {
				list = append(list, pkg{f[0], f[1]})
			}
		case inBlock:
			if f := strings.Fields(line); len(f) >= 2 {
				list = append(list, pkg{f[0], f[1]})
			}
		}
	}
	return sendPackages(list), nil
}

// ---------------------------------------------------------------- transport

func (s *sdk) enqueue(path string, body any) {
	if s.off.Load() || s.closed.Load() {
		return
	}
	s.pending.Add(1)
	defer func() {
		if recover() != nil { // send on closed channel after Close/re-Init
			s.pending.Add(-1)
		}
	}()
	select {
	case s.queue <- job{path, body}:
	default:
		s.pending.Add(-1) // queue full: drop rather than block the caller
		s.dropped.Add(1)
	}
}

func (s *sdk) worker() {
	for j := range s.queue {
		taken := int64(1)
		if j.path == eventsPath {
			batch := []any{j.body}
			size := sizeOf(j.body)
		fill:
			for len(batch) < s.opts.BatchSize && size < batchBytes {
				select {
				case n, ok := <-s.queue:
					if !ok {
						break fill
					}
					taken++
					if n.path != eventsPath {
						s.send(n.path, n.body)
						continue
					}
					batch = append(batch, n.body)
					size += sizeOf(n.body)
				default:
					break fill
				}
			}
			s.sendEvents(batch)
		} else {
			s.send(j.path, j.body)
		}
		s.pending.Add(-taken)
	}
}

func sizeOf(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func (s *sdk) sendEvents(events []any) {
	if s.off.Load() {
		return
	}
	if res := s.send(eventsPath, map[string]any{"events": events}); res == resSplit {
		mid := len(events) / 2
		s.sendEvents(events[:mid])
		s.sendEvents(events[mid:])
	}
}

type result int

const (
	resDone result = iota
	resSplit
)

func (s *sdk) send(path string, body any) result {
	if s.off.Load() {
		return resDone
	}
	data, err := json.Marshal(body)
	if err != nil {
		return resDone
	}
	for i := 0; i <= s.opts.Retries; i++ {
		var wait time.Duration
		req, err := http.NewRequest(http.MethodPost, s.opts.Endpoint+path, bytes.NewReader(data))
		if err != nil {
			return resDone
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LJ-Key", s.opts.Key)
		req.Header.Set("User-Agent", "linearjeph-go/"+Version)
		ctxReq, cancel := withTimeout(req, s.opts.Timeout)
		resp, err := s.client.Do(ctxReq)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			code := resp.StatusCode
			cancel()
			switch {
			case code < 300:
				return resDone
			case code == 401:
				s.off.Store(true)
				dbg(s, "ingest key rejected (401); sending disabled")
				fmt.Fprintln(os.Stderr, "[linearjeph] ingest key rejected (401); sending disabled")
				return resDone
			case code == 413:
				if m, ok := body.(map[string]any); ok {
					if evs, ok := m["events"].([]any); ok && len(evs) > 1 {
						return resSplit
					}
				}
				return resDone
			case code != 429 && code < 500:
				dbg(s, "%s -> %d dropped", path, code)
				return resDone
			case code == 429:
				if sec, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && sec > 0 {
					if sec > 60 {
						sec = 60
					}
					wait = time.Duration(sec) * time.Second
				}
			}
		} else {
			cancel()
		}
		if i < s.opts.Retries {
			if wait == 0 {
				wait = (s.opts.Backoff << uint(i)) / 2
				wait += time.Duration(rand.Int63n(int64(wait) + 1))
			}
			time.Sleep(wait)
		}
	}
	dbg(s, "gave up sending to %s", path)
	return resDone
}

// ---------------------------------------------------------------- net/http middleware

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer (Hijack, SetWriteDeadline, ...).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// RecoverOptions tunes RecoverWith.
type RecoverOptions struct {
	// Name builds the transaction name. Default: "<METHOD> <mux pattern>" (Go 1.23+ ServeMux patterns) or
	// "<METHOD> <path>" when the pattern is unknown. Use it to avoid one transaction per id in the path.
	Name func(*http.Request) string
	// IgnorePaths are path prefixes that are neither timed nor reported (health checks).
	IgnorePaths []string
	// NoRepanic answers 500 instead of re-panicking after capture (re-panicking lets net/http log and abort).
	NoRepanic bool
}

func defaultName(r *http.Request) string {
	if f := reflect.ValueOf(r).Elem().FieldByName("Pattern"); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
		p := f.String()
		if strings.Contains(p, " ") {
			return p
		}
		return r.Method + " " + p
	}
	return r.Method + " " + r.URL.Path
}

// Recover wraps a handler: it times each request (transaction) and captures panics, then re-panics so net/http logs
// and aborts the connection exactly as without the SDK.
func Recover(next http.Handler) http.Handler { return RecoverWith(next, RecoverOptions{}) }

// RecoverWith is Recover with options.
func RecoverWith(next http.Handler, o RecoverOptions) http.Handler {
	name := o.Name
	if name == nil {
		name = defaultName
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range o.IgnorePaths {
			if strings.HasPrefix(r.URL.Path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		start := time.Now()
		rec := &recorder{ResponseWriter: w, status: 200}
		defer func() {
			p := recover()
			status := "ok"
			if p != nil {
				status = "error"
				if p != http.ErrAbortHandler {
					func() {
						defer func() { _ = recover() }()
						if s := get(); s != nil {
							emit(s, errorEvent(s, "panic", fmt.Sprint(p), stack(3)), map[string]string{"http.method": r.Method}, s.opts.SampleRate)
						}
					}()
				}
			} else if rec.status >= 500 {
				status = "error"
			}
			func() {
				defer func() { _ = recover() }()
				CaptureTransaction(name(r), time.Since(start), status)
			}()
			if p != nil {
				if o.NoRepanic && p != http.ErrAbortHandler {
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				}
				panic(p)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}
