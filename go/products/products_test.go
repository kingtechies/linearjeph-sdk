package products

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeKey() string { return "ljk_" + "test1234_" + strings.Repeat("x", 32) }

type seen struct {
	method, path, query, auth, ua, body string
}

func server(t *testing.T, h func(w http.ResponseWriter, r *http.Request, s seen)) (*Client, *[]seen) {
	t.Helper()
	var log []seen
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("User-Agent"), string(b)}
		log = append(log, s)
		h(w, r, s)
	}))
	t.Cleanup(ts.Close)
	c, err := New(Options{APIKey: fakeKey(), BaseURL: ts.URL, Backoff: time.Millisecond, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c, &log
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestHeadersAndWhoami(t *testing.T) {
	c, log := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		writeJSON(w, 200, map[string]any{"organization": map[string]any{"id": 1, "name": "Acme"}, "scopes": []string{"fix"}})
	})
	who, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := (*log)[0]
	if s.path != "/api/v1/apikeys/whoami" || s.auth != "Bearer "+fakeKey() || !strings.HasPrefix(s.ua, "linearjeph-go/") {
		t.Fatalf("%+v", s)
	}
	if Str(who["organization"].(map[string]any), "name") != "Acme" {
		t.Fatal(who)
	}
}

func TestNewRequiresKeyAndReadsEnv(t *testing.T) {
	t.Setenv("LJ_API_KEY", "")
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error")
	}
	t.Setenv("LJ_API_KEY", fakeKey())
	t.Setenv("LJ_BASE_URL", "http://example.invalid/")
	c, err := New(Options{})
	if err != nil || c.base != "http://example.invalid" {
		t.Fatal(c, err)
	}
}

func TestApiError(t *testing.T) {
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		writeJSON(w, 402, map[string]any{"error": map[string]any{"code": "not_entitled", "message": "Buy it first."}})
	})
	_, err := c.Fix.Overview(context.Background())
	var ae *ApiError
	if !errors.As(err, &ae) || ae.Status != 402 || ae.Code != "not_entitled" || ae.Message != "Buy it first." {
		t.Fatalf("%v", err)
	}
}

func TestGetRetriesOn5xxAndHonoursRetryAfter(t *testing.T) {
	var n int32
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		if atomic.AddInt32(&n, 1) < 3 {
			w.Header().Set("Retry-After", "0")
			writeJSON(w, 503, map[string]any{"error": map[string]any{"code": "busy", "message": "x"}})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	j, err := c.Request(context.Background(), "GET", "/x", nil, nil)
	if err != nil || j["ok"] != true || n != 3 {
		t.Fatal(j, err, n)
	}
}

func TestGetGivesUpAfterThreeTries(t *testing.T) {
	var n int32
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		atomic.AddInt32(&n, 1)
		writeJSON(w, 429, map[string]any{"error": map[string]any{"code": "throttled", "message": "slow down"}})
	})
	_, err := c.Request(context.Background(), "GET", "/x", nil, nil)
	var ae *ApiError
	if !errors.As(err, &ae) || ae.Status != 429 || n != 3 {
		t.Fatal(err, n)
	}
}

func TestPostIsNotRetried(t *testing.T) {
	var n int32
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		atomic.AddInt32(&n, 1)
		writeJSON(w, 500, map[string]any{"error": map[string]any{"code": "boom", "message": "x"}})
	})
	if _, err := c.Fix.CancelJob(context.Background(), 1); err == nil || n != 1 {
		t.Fatal(err, n)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	var n int32
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		atomic.AddInt32(&n, 1)
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "Job not found."}})
	})
	if _, err := c.Fix.Job(context.Background(), 9); err == nil || n != 1 {
		t.Fatal(err, n)
	}
}

func TestOrgQueryAndEmptyBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("org") != "7" || r.URL.Query().Get("status") != "open" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		w.WriteHeader(204)
	}))
	defer ts.Close()
	c, _ := New(Options{APIKey: fakeKey(), BaseURL: ts.URL, Org: "7"})
	j, err := c.Request(context.Background(), "GET", "/api/v1/opsagent/incidents", nil, Query{"status": "open"})
	if err != nil || len(j) != 0 {
		t.Fatal(j, err)
	}
}

func TestRouteMap(t *testing.T) {
	c, log := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		writeJSON(w, 200, map[string]any{"results": []any{}})
	})
	ctx := context.Background()
	calls := []struct {
		name         string
		fn           func() error
		method, path string
	}{
		{"fix overview", func() error { _, e := c.Fix.Overview(ctx); return e }, "GET", "/api/v1/fixops/overview"},
		{"fix create", func() error { _, e := c.Fix.CreateJob(ctx, JSON{"source_id": "s", "issue": "bug"}); return e }, "POST", "/api/v1/fixops/jobs"},
		{"fix events", func() error { _, e := c.Fix.JobEvents(ctx, 3, 9); return e }, "GET", "/api/v1/fixops/jobs/3/events"},
		{"fix reject", func() error { _, e := c.Fix.RejectJob(ctx, 3, "no"); return e }, "POST", "/api/v1/fixops/jobs/3/reject"},
		{"fix rollback", func() error { _, e := c.Fix.RollbackJob(ctx, 3); return e }, "POST", "/api/v1/fixops/jobs/3/rollback"},
		{"fix update project", func() error { _, e := c.Fix.UpdateProject(ctx, 2, JSON{"deps_enabled": true}); return e }, "PATCH", "/api/v1/fixops/projects/2"},
		{"fix review", func() error { _, e := c.Fix.Review(ctx, 2); return e }, "POST", "/api/v1/fixops/projects/2/review-now"},
		{"fix deps", func() error { _, e := c.Fix.Deps(ctx, 2); return e }, "POST", "/api/v1/fixops/projects/2/deps-now"},
		{"fix tests", func() error { _, e := c.Fix.Tests(ctx, 2, 4); return e }, "POST", "/api/v1/fixops/projects/2/tests-now"},
		{"fix plan", func() error { _, e := c.Fix.MigrationPlan(ctx, 5); return e }, "POST", "/api/v1/fixops/migrations/5/plan"},
		{"shield vibe scan", func() error { _, e := c.Shield.VibeScan(ctx, 4); return e }, "POST", "/api/v1/shield/vibe/projects/4/scan"},
		{"shield vibe detail", func() error { _, e := c.Shield.VibeScanDetail(ctx, 8); return e }, "GET", "/api/v1/shield/vibe/scans/8"},
		{"shield secrets findings", func() error { _, e := c.Shield.SecretFindings(ctx, "open"); return e }, "GET", "/api/v1/shield/secrets/findings"},
		{"shield pentest scan", func() error { _, e := c.Shield.PentestScan(ctx, 6); return e }, "POST", "/api/v1/shield/pentest/targets/6/scan"},
		{"shield retest", func() error { _, e := c.Shield.PentestRetest(ctx, 6); return e }, "POST", "/api/v1/shield/pentest/findings/6/retest"},
		{"shield waf events", func() error { _, e := c.Shield.WafEvents(ctx, 6); return e }, "GET", "/api/v1/shield/waf/domains/6/events"},
		{"shield security scan", func() error { _, e := c.Shield.SecurityScan(ctx, 6); return e }, "POST", "/api/v1/security/scans"},
		{"ops resolve", func() error { _, e := c.Ops.ResolveIncident(ctx, 2); return e }, "POST", "/api/v1/opsagent/incidents/2/resolve"},
		{"ops cost analyse", func() error { _, e := c.Ops.CostAnalyse(ctx); return e }, "POST", "/api/v1/opsagent/cost/analyse"},
		{"ops managed", func() error { _, e := c.Ops.ManagedDatabases(ctx); return e }, "GET", "/api/v1/opsagent/managed-db"},
		{"ops backup targets", func() error { _, e := c.Ops.BackupTargets(ctx); return e }, "GET", "/api/v1/backups/agent/targets"},
		{"ops drill", func() error { _, e := c.Ops.RunDrill(ctx, 3); return e }, "POST", "/api/v1/backups/agent/targets/3/drill"},
		{"ops runs", func() error { _, e := c.Ops.BackupRuns(ctx, 3); return e }, "GET", "/api/v1/backups/agent/targets/3/runs"},
		{"models endpoints", func() error { _, e := c.Models.Endpoints(ctx); return e }, "GET", "/api/v1/endpoints/endpoints"},
		{"models action", func() error { _, e := c.Models.EndpointAction(ctx, 1, "suspend"); return e }, "POST", "/api/v1/endpoints/endpoints/1/suspend"},
		{"models private logs", func() error { _, e := c.Models.PrivateLogs(ctx, 1); return e }, "GET", "/api/v1/agentcloud/private/endpoints/1/logs"},
		{"models finetune jobs", func() error { _, e := c.Models.FinetuneJobs(ctx); return e }, "GET", "/api/v1/agentcloud/finetune/jobs"},
		{"agents run detail", func() error { _, e := c.Agents.Run(ctx, 1, 9); return e }, "GET", "/api/v1/agentcloud/agents/1/runs/9"},
		{"agents action", func() error { _, e := c.Agents.Action(ctx, 1, "run", JSON{"q": 1}); return e }, "POST", "/api/v1/agentcloud/agents/1/run"},
		{"comply recheck", func() error { _, e := c.Comply.Recheck(ctx); return e }, "POST", "/api/v1/comply/compliance/recheck"},
		{"comply tasks", func() error { _, e := c.Comply.Tasks(ctx); return e }, "GET", "/api/v1/comply/compliance/tasks"},
	}
	for i, tc := range calls {
		if err := tc.fn(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		s := (*log)[i]
		if s.method != tc.method || s.path != tc.path {
			t.Errorf("%s: got %s %s", tc.name, s.method, s.path)
		}
	}
	if b := (*log)[1].body; !strings.Contains(b, `"issue":"bug"`) {
		t.Errorf("body %s", b)
	}
	if q := (*log)[2].query; q != "after=9" {
		t.Errorf("query %s", q)
	}
}

func TestFixWait(t *testing.T) {
	var n int32
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		st := "patching"
		if atomic.AddInt32(&n, 1) >= 3 {
			st = "pr_open"
		}
		writeJSON(w, 200, map[string]any{"id": 5, "status": st})
	})
	j, err := c.Fix.Wait(context.Background(), 5, time.Second)
	if err != nil || Str(j, "status") != "pr_open" || n != 3 {
		t.Fatal(j, err, n)
	}
}

func TestFixWaitTimeout(t *testing.T) {
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		writeJSON(w, 200, map[string]any{"id": 5, "status": "patching"})
	})
	if _, err := c.Fix.Wait(context.Background(), 5, 30*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
}

func TestShieldGate(t *testing.T) {
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {})
	scan := JSON{"findings": []any{
		map[string]any{"severity": "low", "title": "a"},
		map[string]any{"severity": "high", "title": "b"},
		map[string]any{"severity": "critical", "title": "c"},
		map[string]any{"severity": "info", "title": "d"},
	}}
	cases := map[string]int{"": 2, "high": 2, "critical": 1, "medium": 2, "low": 3}
	for lvl, want := range cases {
		got, err := c.Shield.Gate(scan, lvl)
		if err != nil || len(got) != want {
			t.Errorf("%q: %d %v", lvl, len(got), err)
		}
	}
	if _, err := c.Shield.Gate(scan, "severe"); err == nil {
		t.Fatal("expected error")
	}
}

func TestWaitVibeScan(t *testing.T) {
	var n int32
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		st := "running"
		if atomic.AddInt32(&n, 1) >= 2 {
			st = "done"
		}
		writeJSON(w, 200, map[string]any{"id": 8, "status": st, "findings": []any{}})
	})
	j, err := c.Shield.WaitVibeScan(context.Background(), 8, time.Second)
	if err != nil || Str(j, "status") != "done" {
		t.Fatal(j, err)
	}
}

func TestDownloadAndPatch(t *testing.T) {
	c, _ := server(t, func(w http.ResponseWriter, r *http.Request, s seen) {
		w.Header().Set("Content-Type", "text/x-diff")
		_, _ = w.Write([]byte("--- a\n+++ b\n"))
	})
	p, err := c.Fix.JobPatch(context.Background(), 2)
	if err != nil || p != "--- a\n+++ b\n" {
		t.Fatal(p, err)
	}
	j, err := c.Request(context.Background(), "GET", "/x", nil, nil)
	if err != nil || j["raw"] == nil {
		t.Fatal(j, err)
	}
}
