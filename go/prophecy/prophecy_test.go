package prophecy

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rec struct {
	path string
	hdr  http.Header
	body map[string]any
	gz   bool
}

type capture struct {
	mu   sync.Mutex
	reqs []rec
	srv  *httptest.Server
	fail atomic.Int32 // number of upcoming requests to answer 503
}

func newCapture(t *testing.T, reply func(path string) string) *capture {
	c := &capture{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rd io.Reader = r.Body
		gz := r.Header.Get("Content-Encoding") == "gzip"
		if gz {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				w.WriteHeader(400)
				return
			}
			rd = zr
		}
		raw, _ := io.ReadAll(rd)
		m := map[string]any{}
		json.Unmarshal(raw, &m)
		if c.fail.Add(-1) >= 0 {
			w.WriteHeader(503)
			return
		}
		c.mu.Lock()
		c.reqs = append(c.reqs, rec{r.URL.RequestURI(), r.Header.Clone(), m, gz})
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body := `{"accepted":1,"rejected":0}`
		if reply != nil {
			body = reply(r.URL.Path)
		}
		w.WriteHeader(202)
		io.WriteString(w, body)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *capture) list() []rec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]rec(nil), c.reqs...)
}

func opts(c *capture) Options {
	return Options{IngestKey: "lj_app_x", SecretKey: "ljp_sk_secret", Endpoint: c.srv.URL, Backoff: time.Millisecond, FlushInterval: time.Hour}
}

func TestTrackBatchesGzipsHashesAndAuth(t *testing.T) {
	cp := newCapture(t, nil)
	cl, err := New(opts(cp))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		cl.Track("thing", TrackOptions{UID: "user-1", Props: map[string]any{"i": i}})
	}
	if !cl.Close(5 * time.Second) {
		t.Fatal("flush incomplete")
	}
	total := 0
	for _, r := range cp.list() {
		if r.path != "/ingest/v1/prophecy/events" {
			continue
		}
		if !r.gz {
			t.Error("body not gzipped")
		}
		if r.hdr.Get("X-LJ-Key") != "lj_app_x" || r.hdr.Get("Authorization") != "" {
			t.Errorf("bad auth headers: %v", r.hdr)
		}
		evs := r.body["events"].([]any)
		if len(evs) > 100 {
			t.Errorf("batch too large: %d", len(evs))
		}
		total += len(evs)
		e := evs[0].(map[string]any)
		if e["uid"] != HashUID("user-1") || len(e["uid"].(string)) != 64 {
			t.Errorf("uid not hashed: %v", e["uid"])
		}
		if e["kind"] != "custom" {
			t.Errorf("kind %v", e["kind"])
		}
	}
	if total != 250 {
		t.Fatalf("delivered %d of 250", total)
	}
}

func TestHashDisabled(t *testing.T) {
	cp := newCapture(t, nil)
	o := opts(cp)
	o.HashUserIDs = Bool(false)
	cl, _ := New(o)
	cl.Feature("export", "raw-id")
	cl.Close(5 * time.Second)
	e := cp.list()[0].body["events"].([]any)[0].(map[string]any)
	if e["uid"] != "raw-id" || e["kind"] != "feature" || e["feature"] != "export" {
		t.Fatalf("%v", e)
	}
}

func TestHelpersShapeEvents(t *testing.T) {
	cp := newCapture(t, nil)
	cl, _ := New(opts(cp))
	cl.Signup("u1", SignupOptions{Plan: "free", Channel: "ads", Country: "NG"})
	cl.Payment("u1", 49.5, PaymentOptions{Currency: "USD", Plan: "pro"})
	cl.Payment("u1", -10, PaymentOptions{})
	cl.Subscription("u1", "pro", 49.5, "active")
	cl.Churn("u1", "price")
	cl.Identify("u1", map[string]any{"company": "Acme", "plan": "pro"})
	cl.Close(5 * time.Second)
	kinds := map[string]map[string]any{}
	var custs []any
	for _, r := range cp.list() {
		switch r.path {
		case "/ingest/v1/prophecy/events":
			for _, e := range r.body["events"].([]any) {
				m := e.(map[string]any)
				kinds[m["kind"].(string)] = m
			}
		case "/ingest/v1/prophecy/identify":
			custs = append(custs, r.body["customers"].([]any)...)
		}
	}
	if kinds["payment"]["amount"].(float64) != 49.5 || kinds["payment"]["currency"] != "USD" {
		t.Errorf("payment %v", kinds["payment"])
	}
	if kinds["refund"]["amount"].(float64) != -10 {
		t.Errorf("refund %v", kinds["refund"])
	}
	if p := kinds["subscription"]["props"].(map[string]any); p["status"] != "active" || p["mrr"].(float64) != 49.5 {
		t.Errorf("subscription %v", p)
	}
	if kinds["churn"]["props"].(map[string]any)["reason"] != "price" || kinds["signup"]["props"].(map[string]any)["country"] != "NG" {
		t.Errorf("churn/signup %v %v", kinds["churn"], kinds["signup"])
	}
	if len(custs) != 3 {
		t.Fatalf("customers %d", len(custs))
	}
	last := custs[2].(map[string]any)
	if last["plan"] != "pro" || last["traits"].(map[string]any)["company"] != "Acme" {
		t.Errorf("identify %v", last)
	}
}

func TestRetriesOn5xx(t *testing.T) {
	cp := newCapture(t, nil)
	cp.fail.Store(2)
	cl, _ := New(opts(cp))
	cl.Track("x", TrackOptions{})
	cl.Close(5 * time.Second)
	if len(cp.list()) != 1 {
		t.Fatalf("expected 1 successful delivery after retries, got %d", len(cp.list()))
	}
}

func TestQueueBounded(t *testing.T) {
	cp := newCapture(t, nil)
	o := opts(cp)
	o.QueueSize = 10
	cl, _ := New(o)
	for i := 0; i < 25; i++ {
		cl.Track("x", TrackOptions{})
	}
	if cl.Dropped() != 15 {
		t.Fatalf("dropped %d", cl.Dropped())
	}
	cl.Close(5 * time.Second)
}

func TestNeverPanicsOrBlocks(t *testing.T) {
	// unreachable endpoint, nil props, unmarshalable value, zero values, closed client
	o := Options{IngestKey: "k", Endpoint: "http://127.0.0.1:1", Retries: -1, Backoff: time.Millisecond, FlushInterval: time.Hour}
	cl, _ := New(o)
	cl.Track("", TrackOptions{})
	cl.Track("x", TrackOptions{Props: map[string]any{"bad": make(chan int)}})
	cl.Identify("", nil)
	cl.Signup("", SignupOptions{})
	cl.RecordRequest("", "", 0, 0)
	cl.Flush(time.Second)
	cl.Close(time.Second)
	cl.Track("after-close", TrackOptions{})
	// package-level calls before Init are no-ops
	curMu.Lock()
	cur = nil
	curMu.Unlock()
	Track("x", TrackOptions{})
	Feature("x", "u")
	Flush(time.Second)
	if _, err := Report(""); err == nil {
		t.Error("Report before Init should error")
	}
}

func TestInitRequiresKey(t *testing.T) {
	if err := Init(Options{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSecretAPI(t *testing.T) {
	cp := newCapture(t, func(p string) string {
		switch p {
		case "/api/v1/prophecy/sdk/embed":
			return `{"url":"https://linearjeph.cc/prophecy-embed/tok","expires_at":"x"}`
		case "/api/v1/prophecy/sdk/import":
			return `{"job":7,"accepted":2,"rejected":1,"errors":["row 3 bad"]}`
		}
		return `{"ok":true,"report":{}}`
	})
	cl, _ := New(opts(cp))
	if _, err := cl.Report("revenue"); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Live(); err != nil {
		t.Fatal(err)
	}
	u, err := cl.EmbedURL(600)
	if err != nil || !strings.HasSuffix(u, "/tok") {
		t.Fatalf("%q %v", u, err)
	}
	reqs := cp.list()
	want := []string{"/api/v1/prophecy/sdk/report?section=revenue", "/api/v1/prophecy/sdk/refresh", "/api/v1/prophecy/sdk/live", "/api/v1/prophecy/sdk/embed"}
	for i, w := range want {
		if reqs[i].path != w {
			t.Errorf("req %d path %s want %s", i, reqs[i].path, w)
		}
		if reqs[i].hdr.Get("Authorization") != "Bearer ljp_sk_secret" || reqs[i].hdr.Get("X-LJ-Key") != "" {
			t.Errorf("auth headers %v", reqs[i].hdr)
		}
	}
	if reqs[3].body["ttl"].(float64) != 600 {
		t.Error("ttl")
	}
	// no secret key
	c2, _ := New(Options{IngestKey: "k", Endpoint: cp.srv.URL})
	if _, err := c2.Report(""); err == nil {
		t.Error("expected missing secret error")
	}
	c2.Close(time.Second)
	cl.Close(time.Second)
}

func TestImportBatching(t *testing.T) {
	cp := newCapture(t, func(p string) string { return `{"job":1,"accepted":5000,"rejected":0,"errors":[]}` })
	cl, _ := New(opts(cp))
	rows := make([]map[string]any, 12000)
	for i := range rows {
		rows[i] = map[string]any{"uid": "u", "name": "x", "ts": "2024-01-01T00:00:00Z", "id": i}
	}
	var prog []int
	res, err := cl.ImportHistory("events", rows, func(d, tot int) { prog = append(prog, d) })
	if err != nil {
		t.Fatal(err)
	}
	reqs := cp.list()
	if len(reqs) != 3 {
		t.Fatalf("batches %d", len(reqs))
	}
	if len(reqs[0].body["rows"].([]any)) != 5000 || len(reqs[2].body["rows"].([]any)) != 2000 {
		t.Error("batch sizes")
	}
	if reqs[0].body["kind"] != "events" || reqs[0].body["rows"].([]any)[0].(map[string]any)["uid"] != HashUID("u") {
		t.Error("kind/uid hashing")
	}
	if len(prog) != 3 || prog[2] != 12000 || res.Accepted != 15000 || len(res.Jobs) != 3 {
		t.Errorf("progress %v res %+v", prog, res)
	}
	if _, err := cl.ImportHistory("bogus", nil, nil); err == nil {
		t.Error("bad kind")
	}
	cl.Close(time.Second)
}

func TestManifestSend(t *testing.T) {
	cp := newCapture(t, nil)
	cl, _ := New(opts(cp))
	if err := cl.SendManifest(Manifest{Schema: 1, RootName: "x"}); err != nil {
		t.Fatal(err)
	}
	r := cp.list()[0]
	if r.path != "/ingest/v1/prophecy/manifest" || r.hdr.Get("X-LJ-Key") == "" || r.body["manifest"].(map[string]any)["root_name"] != "x" {
		t.Fatalf("%v", r)
	}
	cl.Close(time.Second)
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/users/42":              "/users/:id",
		"/users/42/orders/7?x=1": "/users/:id/orders/:id",
		"/files/3f2b8c1e-1234-4abc-9def-0123456789ab": "/files/:id",
		"/tokens/0123456789abcdef0123":                "/tokens/:id",
		"/about":                                      "/about",
		"/":                                           "/",
		"":                                            "/",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}

func TestMiddleware(t *testing.T) {
	cp := newCapture(t, nil)
	cl, _ := New(opts(cp))
	mux := http.NewServeMux()
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hi") })
	h := cl.Middleware(mux)
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	defer srv.Close()
	http.Get(srv.URL + "/users/123?secret=1")
	http.Get(srv.URL + "/ok")
	http.Get(srv.URL + "/boom") // server recovers the re-panic and closes the connection
	cl.Close(5 * time.Second)
	got := map[string]map[string]any{}
	for _, r := range cp.list() {
		for _, e := range r.body["events"].([]any) {
			p := e.(map[string]any)["props"].(map[string]any)
			got[p["route"].(string)] = p
			if e.(map[string]any)["kind"] != "request" {
				t.Error("kind")
			}
			if _, has := e.(map[string]any)["uid"]; has {
				t.Error("request events carry no uid")
			}
		}
	}
	if got["/users/:id"]["status"].(float64) != 201 || got["/users/:id"]["method"] != "GET" {
		t.Errorf("users %v", got["/users/:id"])
	}
	if got["/ok"]["status"].(float64) != 200 || got["/boom"]["status"].(float64) != 500 {
		t.Errorf("%v", got)
	}
	if _, ok := got["/users/:id"]["duration_ms"].(float64); !ok {
		t.Error("duration")
	}
}

func TestRouteOfPattern(t *testing.T) {
	r := httptest.NewRequest("GET", "/users/9", nil)
	// without a Pattern field set (or on older Go) the path is normalised
	if routeOf(r) != "/users/:id" {
		t.Errorf("%s", routeOf(r))
	}
}

func TestRecordRequestPackageLevel(t *testing.T) {
	cp := newCapture(t, nil)
	if err := Init(opts(cp)); err != nil {
		t.Fatal(err)
	}
	RecordRequest("/x/:id", "post", 404, 1500*time.Microsecond)
	Close(5 * time.Second)
	p := cp.list()[0].body["events"].([]any)[0].(map[string]any)["props"].(map[string]any)
	if p["method"] != "POST" || p["status"].(float64) != 404 || p["duration_ms"].(float64) != 1.5 {
		t.Errorf("%v", p)
	}
}

func TestTemplateFromPattern(t *testing.T) {
	cases := map[string]string{"GET /users/{id}": "/users/:id", "/files/{path...}": "/files/:path", "POST example.com/a/{x}/b": "/a/:x/b", "/{$}": "/{$}"}
	for in, want := range cases {
		if got := templateFromPattern(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}
