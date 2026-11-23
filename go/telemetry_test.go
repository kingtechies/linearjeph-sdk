package linearjeph

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type gotT struct {
	Path string
	Enc  string
	Body map[string]any
}

type trig struct {
	srv    *httptest.Server
	mu     sync.Mutex
	all    []gotT
	script []int
}

func newTRig(t *testing.T, script ...int) *trig {
	r := &trig{script: script}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		var rd io.Reader = req.Body
		enc := req.Header.Get("Content-Encoding")
		if enc == "gzip" {
			zr, err := gzip.NewReader(req.Body)
			if err != nil {
				w.WriteHeader(400)
				return
			}
			rd = zr
		}
		var b map[string]any
		_ = json.NewDecoder(rd).Decode(&b)
		if len(r.script) > 0 {
			c := r.script[0]
			r.script = r.script[1:]
			if c == 429 {
				w.Header().Set("Retry-After", "0")
			}
			w.WriteHeader(c)
			return
		}
		r.all = append(r.all, gotT{req.URL.Path, enc, b})
		w.WriteHeader(202)
	}))
	t.Cleanup(func() { Close(time.Second); r.srv.Close() })
	return r
}

func (r *trig) init(t *testing.T, mod func(*Options)) {
	o := Options{Key: "lj_app_k", Endpoint: r.srv.URL, Release: "2.0.0", Backoff: time.Millisecond, MetricsInterval: time.Hour}
	if mod != nil {
		mod(&o)
	}
	if err := Init(o); err != nil {
		t.Fatal(err)
	}
}

func (r *trig) items(suffix, key string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, g := range r.all {
		if strings.HasSuffix(g.Path, suffix) {
			if l, ok := g.Body[key].([]any); ok {
				for _, x := range l {
					out = append(out, x.(map[string]any))
				}
			}
		}
	}
	return out
}

func (r *trig) bodies(suffix string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, g := range r.all {
		if strings.HasSuffix(g.Path, suffix) {
			out = append(out, g.Body)
		}
	}
	return out
}

func byName(items []map[string]any, key string) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, it := range items {
		m[it[key].(string)] = it
	}
	return m
}

func TestMetricsAggregateAndFlush(t *testing.T) {
	r := newTRig(t)
	r.init(t, nil)
	for i := 0; i < 5; i++ {
		Incr("jobs.done", 1, map[string]string{"queue": "mail"})
	}
	Incr("jobs.done", 10, map[string]string{"queue": "mail"})
	Gauge("queue.depth", 3, nil)
	Gauge("queue.depth", 9, nil)
	for _, v := range []float64{10, 20, 30} {
		Histogram("lat", v, nil)
	}
	Time("work", nil, func() { time.Sleep(15 * time.Millisecond) })
	flush(t)
	m := byName(r.items("/metrics", "metrics"), "name")
	if m["jobs.done"]["type"] != "counter" || m["jobs.done"]["value"].(float64) != 15 || m["jobs.done"]["tags"].(map[string]any)["queue"] != "mail" {
		t.Fatalf("counter %v", m["jobs.done"])
	}
	if m["queue.depth"]["value"].(float64) != 9 {
		t.Fatalf("gauge %v", m["queue.depth"])
	}
	h := m["lat"]
	if h["count"].(float64) != 3 || h["sum"].(float64) != 60 || h["min"].(float64) != 10 || h["max"].(float64) != 30 || len(h["values"].([]any)) != 3 {
		t.Fatalf("histogram %v", h)
	}
	if m["work"]["min"].(float64) < 10 {
		t.Fatalf("timer %v", m["work"])
	}
}

func TestMetricsSentOnInterval(t *testing.T) {
	r := newTRig(t)
	r.init(t, func(o *Options) { o.MetricsInterval = time.Second })
	Incr("tick", 1, nil)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(r.items("/metrics", "metrics")) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if len(r.items("/metrics", "metrics")) != 1 {
		t.Fatal("metrics were not sent on the timer")
	}
}

func TestMetricValidationSeriesCapAndGzip(t *testing.T) {
	r := newTRig(t)
	r.init(t, func(o *Options) { o.MaxMetricSeries = 3 })
	Incr("bad name!", 1, nil)
	Incr("nan", 0/func() float64 { return 0 }(), nil)
	for _, i := range []string{"0", "1", "2", "3", "4", "5"} {
		Incr("many", 1, map[string]string{"i": i})
	}
	if DroppedMetricSeries() < 3 {
		t.Fatalf("drops %d", DroppedMetricSeries())
	}
	flush(t)
	if n := len(r.items("/metrics", "metrics")); n != 3 {
		t.Fatalf("series %d", n)
	}
	r2 := newTRig(t)
	r2.init(t, func(o *Options) { o.MaxMetricSeries = 1000 })
	for i := 0; i < 300; i++ {
		Incr("series."+itoa(i), 1, map[string]string{"pad": strings.Repeat("x", 50)})
	}
	flush(t)
	gz := false
	for _, g := range r2.all {
		gz = gz || g.Enc == "gzip"
	}
	if !gz || len(r2.items("/metrics", "metrics")) != 300 {
		t.Fatalf("gzip=%v n=%d", gz, len(r2.items("/metrics", "metrics")))
	}
}

func TestTelemetryNeverPanicsWithoutInit(t *testing.T) {
	Close(time.Millisecond)
	Incr("a", 1, nil)
	Gauge("a", 1, nil)
	Histogram("a", 1, nil)
	Log(context.Background(), slog.LevelInfo, "x", nil)
	Checkin("job", "ok", CheckinOptions{})
	Release("1", "", "")
	Deploy(DeployOptions{})
	ctx, sp := StartSpan(context.Background(), "s")
	sp.End()
	if SpanFromContext(ctx) != sp {
		t.Fatal("span must be in ctx")
	}
	slog.New(NewSlogHandler(nil)).Info("no sdk")
	StartTimer("t", nil).Stop()
}

func TestScrubPII(t *testing.T) {
	cases := map[string]string{
		"mail bob@example.com":        "mail [email]",
		"card 4111 1111 1111 1111 ok": "card [card] ok",
		"order 1234567890123 id":      "order 1234567890123 id",
		"key lj_app_abcdefghijkl":     "key [token]",
		"id 4111111111111111-extra":   "id 4111111111111111-extra",
	}
	for in, want := range cases {
		if got := ScrubPII(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
	if s := ScrubPII("jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig password=hunter2"); strings.Contains(s, "hunter2") || strings.Contains(s, "eyJ") {
		t.Errorf("leaked: %s", s)
	}
}

func TestLogsSlogHandlerScrubAndLevels(t *testing.T) {
	r := newTRig(t)
	r.init(t, func(o *Options) { o.LogLevel = slog.LevelInfo })
	var buf bytes.Buffer
	lg := slog.New(NewSlogHandler(slog.NewTextHandler(&buf, nil))).With("service", "pay")
	lg.Debug("debug line")
	lg.Error("user ann@example.com paid with 4242424242424242", "order", 5, "password", "x", "note", "a@b.co")
	lg.WithGroup("req").Warn("slow", "ms", 812)
	flush(t)
	logs := r.items("/logs", "logs")
	if len(logs) != 2 {
		t.Fatalf("logs %v", logs)
	}
	e := logs[0]
	cx := e["context"].(map[string]any)
	if e["level"] != "error" || e["message"] != "user [email] paid with [card]" || cx["order"].(float64) != 5 || cx["password"] != "[Filtered]" || cx["note"] != "[email]" || cx["service"] != "pay" {
		t.Fatalf("%v", e)
	}
	w := logs[1]
	if w["level"] != "warning" || w["context"].(map[string]any)["req.ms"].(float64) != 812 {
		t.Fatalf("%v", w)
	}
	if !strings.Contains(buf.String(), "debug line") && strings.Contains(buf.String(), "ann@example.com") == false {
		t.Fatalf("next handler must still receive records: %q", buf.String())
	}
}

func TestLogHookAndNoPII(t *testing.T) {
	r := newTRig(t)
	r.init(t, func(o *Options) {
		o.NoPIIScrub = true
		o.BeforeSendLog = func(it LogItem) *LogItem {
			if strings.Contains(it.Message, "drop") {
				return nil
			}
			it.Context["hooked"] = true
			return &it
		}
	})
	Log(context.Background(), slog.LevelInfo, "keep a@b.co", nil)
	Log(context.Background(), slog.LevelInfo, "drop me", nil)
	flush(t)
	logs := r.items("/logs", "logs")
	if len(logs) != 1 || logs[0]["message"] != "keep a@b.co" || logs[0]["context"].(map[string]any)["hooked"] != true {
		t.Fatalf("%v", logs)
	}
}

func TestSpansNestingAndLogCorrelation(t *testing.T) {
	r := newTRig(t)
	r.init(t, nil)
	ctx, root := StartSpan(context.Background(), "handle", WithKind("server"), WithTags(map[string]string{"route": "/x"}))
	_, child := StartSpan(ctx, "db.query", WithKind("client"))
	child.End()
	_, bad := StartSpan(ctx, "boom")
	bad.EndErr(errors.New("k"))
	Log(ctx, slog.LevelError, "inside", nil)
	root.End()
	root.End() // second End is ignored
	flush(t)
	sp := byName(r.items("/spans", "spans"), "name")
	if len(sp) != 3 || len(r.items("/spans", "spans")) != 3 {
		t.Fatalf("spans %v", sp)
	}
	if sp["handle"]["parent_id"] != "" || sp["db.query"]["parent_id"] != sp["handle"]["span_id"] || len(sp["handle"]["span_id"].(string)) != 16 ||
		sp["db.query"]["trace_id"] != sp["handle"]["trace_id"] {
		t.Fatalf("ids %v", sp)
	}
	if sp["boom"]["status"] != "error" || sp["handle"]["status"] != "ok" || sp["db.query"]["kind"] != "client" || sp["handle"]["tags"].(map[string]any)["route"] != "/x" {
		t.Fatalf("%v", sp)
	}
	lg := r.items("/logs", "logs")[0]
	if lg["trace_id"] != sp["handle"]["trace_id"] || lg["span_id"] != sp["handle"]["span_id"] {
		t.Fatalf("log not linked: %v", lg)
	}
}

func TestSamplingAndTraceparent(t *testing.T) {
	r := newTRig(t)
	r.init(t, func(o *Options) { o.TracesRate = 0.000001 })
	_, sp := StartSpan(context.Background(), "dropped")
	sp.End()
	flush(t)
	if len(r.items("/spans", "spans")) != 0 {
		t.Fatal("unsampled span was sent")
	}
	r.init(t, nil)
	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx, c := StartSpan(context.Background(), "continued", WithKind("server"), WithTraceParent(tp))
	h := http.Header{}
	InjectHeaders(ctx, h)
	c.End()
	_, u := StartSpan(context.Background(), "unsampled", WithTraceParent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"))
	u.End()
	flush(t)
	sp2 := byName(r.items("/spans", "spans"), "name")
	if len(sp2) != 1 || sp2["continued"]["trace_id"] != "0af7651916cd43dd8448eb211c80319c" || sp2["continued"]["parent_id"] != "b7ad6b7169203331" {
		t.Fatalf("%v", sp2)
	}
	if got := h.Get("traceparent"); !strings.HasPrefix(got, "00-0af7651916cd43dd8448eb211c80319c-") || !strings.HasSuffix(got, "-01") {
		t.Fatalf("traceparent %q", got)
	}
	if _, _, _, ok := ParseTraceParent("garbage"); ok {
		t.Fatal("garbage parsed")
	}
}

func TestMiddlewareCreatesSpanAndContinuesTrace(t *testing.T) {
	r := newTRig(t)
	r.init(t, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/items/", func(w http.ResponseWriter, req *http.Request) {
		if SpanFromContext(req.Context()) == nil {
			t.Error("handler context has no span")
		}
		w.WriteHeader(500)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/items/5", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	Recover(mux).ServeHTTP(rec, req)
	flush(t)
	sp := r.items("/spans", "spans")
	if len(sp) != 1 || sp[0]["kind"] != "server" || sp[0]["status"] != "error" || sp[0]["trace_id"] != "0af7651916cd43dd8448eb211c80319c" ||
		!strings.HasPrefix(sp[0]["name"].(string), "GET ") || sp[0]["tags"].(map[string]any)["http.status_code"] != "500" {
		t.Fatalf("%v", sp)
	}
	if len(r.items("/events", "events")) != 1 {
		t.Fatal("transaction still expected")
	}
}

func TestTransportInjectsTraceparentAndMakesClientSpan(t *testing.T) {
	r := newTRig(t)
	r.init(t, nil)
	var seen string
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { seen = q.Header.Get("traceparent") }))
	defer down.Close()
	ctx, root := StartSpan(context.Background(), "job")
	req, _ := http.NewRequestWithContext(ctx, "GET", down.URL+"/x", nil)
	resp, err := (&http.Client{Transport: NewTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	root.End()
	flush(t)
	if !strings.HasPrefix(seen, "00-"+root.TraceID+"-") {
		t.Fatalf("downstream saw %q", seen)
	}
	var client map[string]any
	for _, s := range r.items("/spans", "spans") {
		if s["kind"] == "client" {
			client = s
		}
	}
	if client == nil || client["parent_id"] != root.SpanID {
		t.Fatalf("client span missing: %v", r.items("/spans", "spans"))
	}
}

func TestCheckinsAndMonitor(t *testing.T) {
	r := newTRig(t)
	r.init(t, nil)
	err := MonitorWith("nightly-backup", CheckinOptions{Schedule: "0 2 * * *", TZ: "Africa/Lagos", Grace: 10 * time.Minute, MaxRuntime: time.Hour}, func() error {
		time.Sleep(15 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("disk full for ann@example.com")
	if err := Monitor("flaky", 0, func() error { return want }); err != want {
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
	Checkin("manual", "start", CheckinOptions{RunID: "r1"})
	Checkin("bad name!", "ok", CheckinOptions{})
	flush(t)
	ci := r.bodies("/checkin")
	var nb []map[string]any
	for _, b := range ci {
		if b["monitor"] == "nightly-backup" {
			nb = append(nb, b)
		}
	}
	if len(nb) != 2 || nb[0]["status"] != "start" || nb[1]["status"] != "ok" || nb[0]["run_id"] != nb[1]["run_id"] || nb[1]["duration_ms"].(float64) < 10 ||
		nb[0]["schedule"] != "0 2 * * *" || nb[0]["tz"] != "Africa/Lagos" || nb[0]["grace_s"].(float64) != 600 || nb[0]["max_runtime_s"].(float64) != 3600 {
		t.Fatalf("%v", nb)
	}
	var failMsg, panicStatus any
	for _, b := range ci {
		if b["monitor"] == "flaky" && b["status"] == "fail" {
			failMsg = b["message"]
		}
		if b["monitor"] == "panics" && b["status"] == "fail" {
			panicStatus = b["message"]
		}
		if b["monitor"] == "bad name!" {
			t.Fatal("invalid name sent")
		}
	}
	if failMsg != "disk full for [email]" || panicStatus != "panic: x" {
		t.Fatalf("%v %v", failMsg, panicStatus)
	}
	if last := ci[len(ci)-1]; last["monitor"] != "manual" || last["run_id"] != "r1" {
		t.Fatalf("%v", last)
	}
}

func TestReleaseAndDeploy(t *testing.T) {
	r := newTRig(t)
	r.init(t, nil)
	Release("", "abc1234", "hotfix")
	Deploy(DeployOptions{URL: "https://ci.example/1", Notify: true})
	Deploy(DeployOptions{Version: "2.0.1", Environment: "staging", Status: "failed"})
	flush(t)
	rel, dep := r.bodies("/releases"), r.bodies("/deploys")
	if len(rel) != 1 || rel[0]["version"] != "2.0.0" || rel[0]["commit"] != "abc1234" || rel[0]["notes"] != "hotfix" {
		t.Fatalf("%v", rel)
	}
	if len(dep) != 2 || dep[0]["environment"] != "production" || dep[0]["status"] != "success" || dep[0]["notify"] != true || dep[0]["url"] != "https://ci.example/1" ||
		dep[1]["version"] != "2.0.1" || dep[1]["environment"] != "staging" || dep[1]["notify"] != false {
		t.Fatalf("%v", dep)
	}
}

func TestRetryThenDeliverAnd413SplitsLogBatch(t *testing.T) {
	r := newTRig(t, 429, 503)
	r.init(t, nil)
	Incr("retry.me", 1, nil)
	flush(t)
	if len(r.items("/metrics", "metrics")) != 1 {
		t.Fatal("not delivered after retries")
	}
	r2 := newTRig(t, 413)
	r2.init(t, func(o *Options) { o.QueueSize = 20 })
	for _, m := range []string{"l0", "l1", "l2", "l3"} {
		Log(context.Background(), slog.LevelInfo, m, nil)
	}
	flush(t)
	if n := len(r2.items("/logs", "logs")); n != 4 {
		t.Fatalf("expected all four logs after the split, got %d", n)
	}
}

func TestQueueBoundedWhenServerDown(t *testing.T) {
	if err := Init(Options{Key: "lj_app_k", Endpoint: "http://127.0.0.1:1", QueueSize: 5, Retries: -1, Timeout: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(100 * time.Millisecond) })
	for i := 0; i < 500; i++ {
		Log(context.Background(), slog.LevelInfo, "line", nil)
	}
	if Dropped() == 0 {
		t.Fatal("expected drops on a full queue")
	}
}

// ---------------------------------------------------------------- health

func resetHealth() {
	health.Lock()
	health.checks = map[string]healthCheck{}
	health.Unlock()
}

func TestHealthGradesAndStatuses(t *testing.T) {
	resetHealth()
	th := HealthOptions{Warn: Float(200), Fail: Float(1000)}
	RegisterHealthValue("db", func(context.Context) (float64, error) { return 50, nil }, th)
	RegisterHealthValue("slow_db", func(context.Context) (float64, error) { return 450, nil }, th)
	RegisterHealthValue("dead_db", func(context.Context) (float64, error) { return 5000, nil }, th)
	RegisterHealthValue("disk_free", func(context.Context) (float64, error) { return 3, nil }, HealthOptions{Warn: Float(10), Fail: Float(5), Below: true})
	RegisterHealth("cache", func(context.Context) error { return nil }, HealthOptions{})
	RegisterHealth("down", func(context.Context) error { return errors.New("refused") }, HealthOptions{})
	RegisterHealth("boom", func(context.Context) error { panic("bad") }, HealthOptions{})
	RegisterHealth("hang", func(ctx context.Context) error { <-ctx.Done(); time.Sleep(time.Second); return nil }, HealthOptions{Timeout: 100 * time.Millisecond})
	body := RunHealth(context.Background())
	want := map[string]string{"db": "pass", "slow_db": "warn", "dead_db": "fail", "disk_free": "fail", "cache": "pass", "down": "fail", "boom": "fail", "hang": "fail"}
	for k, v := range want {
		if body.Checks[k].Status != v {
			t.Errorf("%s = %s want %s", k, body.Checks[k].Status, v)
		}
	}
	if body.Status != "fail" || *body.Checks["db"].Value != 50 || !strings.Contains(body.Checks["boom"].Message, "panic") || !strings.Contains(body.Checks["hang"].Message, "timed out") {
		t.Fatalf("%+v", body)
	}
	resetHealth()
}

func TestHealthHandlerTokenAndCodes(t *testing.T) {
	resetHealth()
	RegisterHealth("a", func(context.Context) error { return nil }, HealthOptions{})
	h := HealthHandler("s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 401 {
		t.Fatalf("no token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body HealthBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || body.Status != "ok" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %+v", rec.Code, body)
	}
	RegisterHealthValue("lag", func(context.Context) (float64, error) { return 300, nil }, HealthOptions{Warn: Float(100)})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || body.Status != "degraded" {
		t.Fatalf("%d %+v", rec.Code, body)
	}
	RegisterHealth("down", func(context.Context) error { return errors.New("x") }, HealthOptions{})
	rec = httptest.NewRecorder()
	HealthHandler("").ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 {
		t.Fatalf("failing check must be 503, got %d", rec.Code)
	}
	UnregisterHealth("down")
	resetHealth()
}

func TestVersionConstant(t *testing.T) {
	if Version != "1.3.0" {
		t.Fatal(Version)
	}
}
