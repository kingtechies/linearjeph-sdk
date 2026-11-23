package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeKey() string { return "ljk_" + "test1234_" + strings.Repeat("y", 32) }

func j(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func setup(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	t.Setenv("LJ_API_KEY", fakeKey())
	t.Setenv("LJ_BASE_URL", ts.URL)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LJ_POLL_INTERVAL", "5ms")
}

func exec(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestUsage(t *testing.T) {
	if code, _, _ := exec(); code != 2 {
		t.Fatal(code)
	}
	if code, out, _ := exec("--help"); code != 0 || !strings.Contains(out, "shield gate") {
		t.Fatal(code, out)
	}
	if code, _, _ := exec("nope"); code != 2 {
		t.Fatal(code)
	}
}

func TestNoKeyIsExit2(t *testing.T) {
	t.Setenv("LJ_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	if code, _, e := exec("whoami"); code != 2 || !strings.Contains(e, "no API key") {
		t.Fatal(code, e)
	}
}

func TestWhoami(t *testing.T) {
	setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeKey() {
			j(w, 401, map[string]any{"error": map[string]any{"code": "unauthorized", "message": "bad key"}})
			return
		}
		j(w, 200, map[string]any{"organization": map[string]any{"id": 3, "name": "Acme"}, "scopes": []string{"fix", "shield"},
			"key": map[string]any{"prefix": "test1234", "name": "ci"}})
	})
	code, out, _ := exec("whoami")
	if code != 0 || !strings.Contains(out, "Acme") || !strings.Contains(out, "fix, shield") || strings.Contains(out, fakeKey()) {
		t.Fatal(code, out)
	}
	if code, out, _ = exec("whoami", "--json"); code != 0 || !strings.Contains(out, `"scopes"`) {
		t.Fatal(code, out)
	}
	if code, _, e := exec("whoami", "--key", "ljk_"+"wrong000_"+strings.Repeat("z", 32)); code != 2 || !strings.Contains(e, "bad key") {
		t.Fatal(code, e)
	}
}

func gateServer(t *testing.T, severity string) {
	setup(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/shield/vibe/projects/4/scan":
			j(w, 201, map[string]any{"id": 11, "status": "queued"})
		case r.Method == "GET" && r.URL.Path == "/api/v1/shield/vibe/scans/11":
			j(w, 200, map[string]any{"id": 11, "status": "done", "grade": "C", "score": 70,
				"findings": []any{map[string]any{"severity": severity, "title": "Exposed .env", "location": "/.env"}}})
		default:
			j(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": r.URL.Path}})
		}
	})
}

func TestShieldGateFails(t *testing.T) {
	gateServer(t, "high")
	code, out, _ := exec("shield", "gate", "--project", "4", "--fail-on", "high")
	if code != 1 || !strings.Contains(out, "gate failed") {
		t.Fatal(code, out)
	}
}

func TestShieldGatePasses(t *testing.T) {
	gateServer(t, "low")
	code, out, _ := exec("shield", "gate", "--project", "4")
	if code != 0 || !strings.Contains(out, "gate passed") {
		t.Fatal(code, out)
	}
	code, out, _ = exec("shield", "gate", "--project", "4", "--fail-on", "low", "--json")
	var v map[string]any
	if code != 1 || json.Unmarshal([]byte(out), &v) != nil || v["passed"] != false {
		t.Fatal(code, out)
	}
}

func TestShieldGateUsage(t *testing.T) {
	gateServer(t, "low")
	if code, _, _ := exec("shield", "gate"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := exec("shield", "gate", "--project", "4", "--fail-on", "bogus"); code != 2 {
		t.Fatal(code)
	}
}

func fixServer(t *testing.T, final string) {
	polls := 0
	setup(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/fixops/projects/2":
			j(w, 200, map[string]any{"id": 2, "source_id": "gh:acme/app"})
		case r.Method == "POST" && r.URL.Path == "/api/v1/fixops/jobs":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["source_id"] != "gh:acme/app" || b["issue"] != "login crashes" {
				j(w, 400, map[string]any{"error": map[string]any{"code": "invalid", "message": "bad body"}})
				return
			}
			j(w, 201, map[string]any{"id": 9, "status": "queued", "status_label": "Queued"})
		case r.URL.Path == "/api/v1/fixops/jobs/9":
			polls++
			st := "patching"
			if polls >= 2 {
				st = final
			}
			j(w, 200, map[string]any{"id": 9, "status": st, "status_label": st, "pr_url": "https://example.test/pr/1"})
		}
	})
}

func TestFixRunWait(t *testing.T) {
	fixServer(t, "pr_open")
	code, out, _ := exec("fix", "run", "--project", "2", "--issue", "login crashes", "--wait")
	if code != 0 || !strings.Contains(out, "pull request: https://example.test/pr/1") {
		t.Fatal(code, out)
	}
}

func TestFixRunFailedIsExit1(t *testing.T) {
	fixServer(t, "failed")
	if code, _, _ := exec("fix", "run", "--project", "2", "--issue", "login crashes", "--wait"); code != 1 {
		t.Fatal(code)
	}
}

func TestFixRunNoWaitAndUsage(t *testing.T) {
	fixServer(t, "done")
	code, out, _ := exec("fix", "run", "--project", "2", "--issue", "login crashes")
	if code != 0 || !strings.Contains(out, "job 9") {
		t.Fatal(code, out)
	}
	if code, _, _ := exec("fix", "run", "--project", "2"); code != 2 {
		t.Fatal(code)
	}
}
