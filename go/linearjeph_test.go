package linearjeph

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type got struct {
	Path string
	Key  string
	Body map[string]any
}

type rig struct {
	srv  *httptest.Server
	mu   sync.Mutex
	all  []got
	hits []int
	// script statuses answered first (value 429 uses Retry-After: 0)
	script []int
}

func newRig(t *testing.T, script ...int) *rig {
	r := &rig{script: script}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.script) > 0 {
			c := r.script[0]
			r.script = r.script[1:]
			r.hits = append(r.hits, c)
			if c == 429 {
				w.Header().Set("Retry-After", "0")
			}
			w.WriteHeader(c)
			return
		}
		var b map[string]any
		_ = json.NewDecoder(req.Body).Decode(&b)
		r.hits = append(r.hits, 202)
		r.all = append(r.all, got{req.URL.Path, req.Header.Get("X-LJ-Key"), b})
		w.WriteHeader(202)
	}))
	t.Cleanup(func() { Close(time.Second); r.srv.Close() })
	return r
}

func (r *rig) events() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, g := range r.all {
		if evs, ok := g.Body["events"].([]any); ok {
			for _, e := range evs {
				out = append(out, e.(map[string]any))
			}
		}
	}
	return out
}

func (r *rig) init(t *testing.T, mod func(*Options)) {
	o := Options{Key: "lj_app_k", Endpoint: r.srv.URL, Release: "1.0.0", Backoff: time.Millisecond}
	if mod != nil {
		mod(&o)
	}
	if err := Init(o); err != nil {
		t.Fatal(err)
	}
	SetUser("")
	scope.Lock()
	scope.tags = map[string]string{}
	scope.Unlock()
}

func flush(t *testing.T) {
	t.Helper()
	if !Flush(5 * time.Second) {
		t.Fatal("flush timeout")
	}
}

func TestCaptureErrorPayloadAndRetry(t *testing.T) {
	r := newRig(t, 503, 503)
	r.init(t, nil)
	CaptureError(errors.New("boom"))
	flush(t)
	if len(r.all) != 1 || r.all[0].Key != "lj_app_k" || r.all[0].Path != "/ingest/v1/events" {
		t.Fatalf("got %+v", r.all)
	}
	ev := r.events()[0]
	ex := ev["exception"].(map[string]any)
	if ev["type"] != "error" || ev["release"] != "1.0.0" || ev["environment"] != "production" || ex["value"] != "boom" {
		t.Fatalf("bad event %+v", ev)
	}
	fr := ex["stacktrace"].([]any)
	last := fr[len(fr)-1].(map[string]any)
	for _, k := range []string{"filename", "function", "lineno", "in_app"} {
		if _, ok := last[k]; !ok {
			t.Fatalf("frame missing %s", k)
		}
	}
	if len(r.hits) != 3 {
		t.Fatalf("hits %v", r.hits)
	}
}

func TestStatusHandling(t *testing.T) {
	r := newRig(t, 429, 400, 413)
	r.init(t, nil)
	CaptureMessage("a") // 429 then retried -> 400 dropped
	flush(t)
	CaptureMessage("b") // 413 on a single event: dropped
	flush(t)
	if len(r.hits) != 3 || len(r.all) != 0 {
		t.Fatalf("hits %v got %d", r.hits, len(r.all))
	}
	r2 := newRig(t, 401)
	r2.init(t, nil)
	CaptureMessage("x")
	flush(t)
	CaptureMessage("y")
	time.Sleep(50 * time.Millisecond)
	if len(r2.hits) != 1 || !get().off.Load() {
		t.Fatalf("401 must disable sending: %v", r2.hits)
	}
}

func TestGivesUpAfterRetries(t *testing.T) {
	r := newRig(t, 500, 500, 500, 500, 500, 500)
	r.init(t, func(o *Options) { o.Retries = 2 })
	CaptureMessage("x")
	flush(t)
	if len(r.hits) != 3 {
		t.Fatalf("hits %v", r.hits)
	}
}

func TestUnreachableEndpointNeverBlocksOrPanics(t *testing.T) {
	if err := Init(Options{Key: "k", Endpoint: "http://127.0.0.1:9", Retries: 1, Backoff: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	CaptureMessage("nobody home")
	flush(t)
	Close(time.Second)
}

func TestQueueIsBoundedAndNeverBlocks(t *testing.T) {
	s := &sdk{opts: Options{QueueSize: 2}, queue: make(chan job, 2)}
	for i := 0; i < 5; i++ {
		s.enqueue("/x", nil)
	}
	if len(s.queue) != 2 || s.dropped.Load() != 3 || s.pending.Load() != 2 {
		t.Fatalf("queue=%d dropped=%d pending=%d", len(s.queue), s.dropped.Load(), s.pending.Load())
	}
}

func TestEventsAreBatched(t *testing.T) {
	r := newRig(t)
	r.init(t, func(o *Options) { o.BatchSize = 50 })
	s := get()
	s.pending.Add(1) // hold the worker's attention until all are queued
	for i := 0; i < 10; i++ {
		CaptureMessage("m")
	}
	s.pending.Add(-1)
	flush(t)
	if len(r.events()) != 10 {
		t.Fatalf("events %d", len(r.events()))
	}
}

func TestHeartbeatAndValidation(t *testing.T) {
	r := newRig(t)
	r.init(t, nil)
	Heartbeat("job", "ok", 60)
	Heartbeat("bad name!", "ok", 60)
	Heartbeat("j2", "weird", 5)
	flush(t)
	if len(r.all) != 2 {
		t.Fatalf("got %+v", r.all)
	}
	b := r.all[0].Body
	if r.all[0].Path != "/ingest/v1/heartbeat" || b["name"] != "job" || b["status"] != "ok" || b["interval_s"].(float64) != 60 {
		t.Fatalf("got %+v", r.all[0])
	}
	if b := r.all[1].Body; b["status"] != "error" || b["interval_s"].(float64) != 30 {
		t.Fatalf("got %+v", b)
	}
}

func TestMonitor(t *testing.T) {
	r := newRig(t)
	r.init(t, nil)
	if err := Monitor("good", 3600, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := errors.New("nope")
	if err := Monitor("bad", 0, func() error { return want }); err != want {
		t.Fatal("error must pass through")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic must propagate")
			}
		}()
		_ = Monitor("panics", 0, func() error { panic("x") })
	}()
	flush(t)
	st := map[string]any{}
	for _, g := range r.all {
		st[g.Body["name"].(string)] = g.Body["status"]
	}
	if st["good"] != "ok" || st["bad"] != "error" || st["panics"] != "error" {
		t.Fatalf("%v", st)
	}
}

func TestRecoverTimesAndCapturesPanic(t *testing.T) {
	r := newRig(t)
	r.init(t, nil)
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/panic" {
			panic("kaboom")
		}
		if req.URL.Path == "/fail" {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ok", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/fail", nil))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected re-panic")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/panic", nil))
	}()
	flush(t)
	var ok, fail, pan bool
	for _, m := range r.events() {
		if m["type"] == "transaction" {
			tr := m["transaction"].(map[string]any)
			ok = ok || (tr["name"] == "GET /ok" && tr["status"] == "ok")
			fail = fail || (tr["name"] == "GET /fail" && tr["status"] == "error")
		}
		if m["type"] == "error" && m["exception"].(map[string]any)["type"] == "panic" {
			pan = true
			for _, f := range m["exception"].(map[string]any)["stacktrace"].([]any) {
				fn := f.(map[string]any)["function"].(string)
				if strings.HasPrefix(fn, "runtime.") {
					t.Errorf("runtime frame leaked: %s", fn)
				}
			}
		}
	}
	if !ok || !fail || !pan {
		t.Fatalf("ok=%v fail=%v panic=%v", ok, fail, pan)
	}
}

func TestRecoverWithOptions(t *testing.T) {
	r := newRig(t)
	r.init(t, nil)
	h := RecoverWith(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/boom/1" {
			panic("x")
		}
	}), RecoverOptions{IgnorePaths: []string{"/health"}, NoRepanic: true,
		Name: func(req *http.Request) string { return req.Method + " /boom/{id}" }})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/health", nil))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/boom/1", nil))
	if w.Code != 500 {
		t.Fatalf("code %d", w.Code)
	}
	flush(t)
	for _, m := range r.events() {
		if m["type"] == "transaction" && m["transaction"].(map[string]any)["name"] != "GET /boom/{id}" {
			t.Fatalf("unexpected txn %+v", m)
		}
	}
	if len(r.events()) != 2 {
		t.Fatalf("want error+transaction, got %d", len(r.events()))
	}
}

func TestScrubbingAndTags(t *testing.T) {
	r := newRig(t)
	r.init(t, func(o *Options) { o.ScrubKeys = []string{"ssn"} })
	SetTag("password", "hunter2")
	SetTag("ssn", "1")
	SetTag("region", "eu")
	SetUser("u-1")
	CaptureMessage("failed password=hunter2 Bearer abcdefghijklmnop")
	CaptureErrorTags(errors.New("e"), map[string]string{"api_key": "k"})
	flush(t)
	evs := r.events()
	tags := evs[0]["tags"].(map[string]any)
	if tags["password"] != "[Filtered]" || tags["ssn"] != "[Filtered]" || tags["region"] != "eu" {
		t.Fatalf("tags %v", tags)
	}
	if evs[1]["tags"].(map[string]any)["api_key"] != "[Filtered]" {
		t.Fatal("api_key not filtered")
	}
	raw, _ := json.Marshal(evs[0])
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "abcdefghijklmnop") {
		t.Fatalf("secret leaked: %s", raw)
	}
	if evs[0]["user"].(map[string]any)["id"] != "u-1" {
		t.Fatal("user missing")
	}
	out := Scrub(map[string]any{"h": map[string]any{"Cookie": "a", "ok": 1}}).(map[string]any)["h"].(map[string]any)
	if out["Cookie"] != "[Filtered]" || out["ok"] != 1 {
		t.Fatalf("%v", out)
	}
}

func TestBeforeSendAndSampling(t *testing.T) {
	r := newRig(t)
	r.init(t, func(o *Options) {
		o.BeforeSend = func(e Event) Event {
			if e["type"] == "transaction" {
				return nil
			}
			if tags, ok := e["tags"].(map[string]string); ok {
				tags["seen"] = "yes"
			}
			return e
		}
	})
	CaptureTransaction("t", time.Millisecond, "ok")
	CaptureMessage("kept")
	flush(t)
	evs := r.events()
	if len(evs) != 1 || evs[0]["tags"].(map[string]any)["seen"] != "yes" {
		t.Fatalf("%v", evs)
	}
	r.init(t, func(o *Options) { o.BeforeSend = func(Event) Event { panic("hook bug") } })
	CaptureMessage("lost")
	flush(t)
	if len(r.events()) != 1 {
		t.Fatal("panicking BeforeSend must drop the event")
	}
	r.init(t, func(o *Options) { o.SampleRate = 0.0000001 })
	for i := 0; i < 50; i++ {
		CaptureMessage("sampled out")
	}
	CaptureTransaction("tx", time.Millisecond, "ok") // TracesRate defaults to 1
	flush(t)
	if n := len(r.events()); n != 2 {
		t.Fatalf("want 1 earlier + 1 transaction, got %d", n)
	}
}

func TestStartTransactionAndCapture(t *testing.T) {
	r := newRig(t)
	r.init(t, nil)
	tx := StartTransaction("job")
	time.Sleep(12 * time.Millisecond)
	tx.EndErr(errors.New("x"))
	tx.End("ok") // ignored: only the first End counts
	Capture("a string")
	Capture(nil)
	Capture(errors.New("wrapped"))
	flush(t)
	var seenTx bool
	var errs int
	for _, m := range r.events() {
		if m["type"] == "transaction" {
			tr := m["transaction"].(map[string]any)
			seenTx = tr["name"] == "job" && tr["status"] == "error" && tr["duration_ms"].(float64) >= 10
		} else {
			errs++
		}
	}
	if !seenTx || errs != 2 {
		t.Fatalf("tx=%v errs=%d", seenTx, errs)
	}
}

func TestReportPackagesFromBuildInfoGoSumAndGoMod(t *testing.T) {
	r := newRig(t)
	r.init(t, nil)
	_ = ReportPackages() // test binaries may report 0 deps: must not panic
	dir := t.TempDir()
	sum := filepath.Join(dir, "go.sum")
	_ = os.WriteFile(sum, []byte("example.com/a v1.2.3 h1:x=\nexample.com/a v1.2.3/go.mod h1:y=\nexample.com/b v0.0.0-2020+incompatible h1:z=\n"), 0o600)
	n, err := ReportGoSum(sum)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	mod := filepath.Join(dir, "go.mod")
	_ = os.WriteFile(mod, []byte("module x\n\ngo 1.21\n\nrequire example.com/c v1.0.0 // indirect\n\nrequire (\n\texample.com/d v2.0.1+incompatible\n)\n"), 0o600)
	n, err = ReportGoMod(mod)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err = ReportGoSum(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file must return an error")
	}
	flush(t)
	var pk []any
	for _, g := range r.all {
		if g.Path == "/ingest/v1/packages" && g.Body["ecosystem"] == "Go" {
			pk = append(pk, g.Body["packages"])
		}
	}
	if len(pk) < 2 {
		t.Fatalf("packages not sent: %+v", r.all)
	}
}

func TestNeverPanicsWithoutInit(t *testing.T) {
	Close(time.Second)
	CaptureError(errors.New("x"))
	CaptureMessage("x")
	CaptureTransaction("x", time.Second, "ok")
	Heartbeat("x", "ok", 1)
	Capture(1)
	if ReportPackages() != 0 || !Flush(time.Millisecond) {
		t.Fatal("unexpected")
	}
	if Init(Options{}) == nil {
		t.Fatal("missing key must be reported")
	}
}
