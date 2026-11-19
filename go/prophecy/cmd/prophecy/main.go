// Command prophecy scans a codebase into a Prophecy manifest and imports history files.
//
//	prophecy scan [dir] [--send]
//	prophecy import <events|customers> <file.csv|file.json>
//
// Keys come from LINEARJEPH_INGEST_KEY, LINEARJEPH_SECRET_KEY and LINEARJEPH_ENDPOINT.
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/kingtechies/linearjeph-sdk/go/prophecy"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func client() (*prophecy.Client, error) {
	return prophecy.New(prophecy.Options{
		IngestKey: os.Getenv("LINEARJEPH_INGEST_KEY"),
		SecretKey: os.Getenv("LINEARJEPH_SECRET_KEY"),
		Endpoint:  os.Getenv("LINEARJEPH_ENDPOINT"),
	})
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: prophecy scan [dir] [--send]\n       prophecy import <events|customers> <file.csv|file.json>")
}

func run(args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		usage(errw)
		return 2
	}
	switch args[0] {
	case "scan":
		dir, send := ".", false
		for _, a := range args[1:] {
			if a == "--send" {
				send = true
			} else {
				dir = a
			}
		}
		m, err := prophecy.Scan(dir)
		if err != nil {
			fmt.Fprintln(errw, "scan failed:", err)
			return 1
		}
		if send {
			c, err := client()
			if err != nil {
				fmt.Fprintln(errw, err)
				return 1
			}
			defer c.Close(0)
			if err := c.SendManifest(m); err != nil {
				fmt.Fprintln(errw, "send failed:", err)
				return 1
			}
			fmt.Fprintf(out, "manifest sent: %d files in %d languages, %d routes, %d dependencies\n", sum(m.Languages), len(m.Languages), len(m.Routes), len(m.Dependencies))
			return 0
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(m)
		return 0
	case "import":
		if len(args) != 3 {
			usage(errw)
			return 2
		}
		rows, err := loadRows(args[2], args[1])
		if err != nil {
			fmt.Fprintln(errw, "read failed:", err)
			return 1
		}
		c, err := client()
		if err != nil {
			fmt.Fprintln(errw, err)
			return 1
		}
		defer c.Close(0)
		res, err := c.ImportHistory(args[1], rows, func(done, total int) {
			fmt.Fprintf(errw, "imported %d/%d\n", done, total)
		})
		if err != nil {
			fmt.Fprintln(errw, "import failed:", err)
			return 1
		}
		fmt.Fprintf(out, "accepted %d, rejected %d\n", res.Accepted, res.Rejected)
		for _, e := range res.Errors {
			fmt.Fprintln(errw, " ", e)
		}
		return 0
	}
	usage(errw)
	return 2
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

var topLevel = map[string]bool{"ts": true, "uid": true, "name": true, "kind": true, "feature": true, "amount": true,
	"currency": true, "id": true, "signup_at": true, "plan": true, "mrr": true, "country": true, "channel": true}

func loadRows(path, kind string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		var raw any
		if err := json.NewDecoder(f).Decode(&raw); err != nil {
			return nil, err
		}
		var list []any
		switch v := raw.(type) {
		case []any:
			list = v
		case map[string]any:
			list, _ = v["rows"].([]any)
		}
		var rows []map[string]any
		for _, x := range list {
			if m, ok := x.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
		if rows == nil {
			return nil, errors.New("expected a JSON array of objects (or {\"rows\": [...]})")
		}
		return rows, nil
	}
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		row := map[string]any{}
		props := map[string]any{}
		for i, h := range head {
			if i >= len(rec) || rec[i] == "" {
				continue
			}
			h = strings.TrimSpace(h)
			v := rec[i]
			switch {
			case h == "amount" || h == "mrr":
				if n, err := strconv.ParseFloat(v, 64); err == nil {
					row[h] = n
				}
			case strings.HasPrefix(h, "props."):
				props[strings.TrimPrefix(h, "props.")] = v
			case topLevel[h]:
				row[h] = v
			default:
				props[h] = v
			}
		}
		if len(props) > 0 {
			key := "props"
			if kind == "customers" {
				key = "traits"
			}
			row[key] = props
		}
		rows = append(rows, row)
	}
	return rows, nil
}
