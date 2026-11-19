package linearjeph

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type spanKey struct{}

var traceparentRe = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// Span is one timed operation inside a trace. Create it with StartSpan; End it exactly once (extra calls are ignored).
type Span struct {
	Name     string
	Kind     string // internal, server, client, producer, consumer
	TraceID  string
	SpanID   string
	ParentID string
	Sampled  bool

	mu     sync.Mutex
	tags   map[string]string
	status string
	start  time.Time
	wall   string
	done   bool
}

// SpanOption customises StartSpan.
type SpanOption func(*spanCfg)

type spanCfg struct {
	kind        string
	tags        map[string]string
	traceparent string
}

// WithKind sets the span kind ("server", "client", ...).
func WithKind(k string) SpanOption { return func(c *spanCfg) { c.kind = k } }

// WithTags adds tags to the span.
func WithTags(t map[string]string) SpanOption { return func(c *spanCfg) { c.tags = t } }

// WithTraceParent continues a trace from an incoming W3C `traceparent` header (used when ctx has no active span).
func WithTraceParent(h string) SpanOption { return func(c *spanCfg) { c.traceparent = h } }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(rand.Intn(256))
		}
	}
	return hex.EncodeToString(b)
}

// ParseTraceParent parses a W3C traceparent header: trace id, parent span id, sampled flag.
func ParseTraceParent(h string) (traceID, parentID string, sampled bool, ok bool) {
	m := traceparentRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(h)))
	if m == nil {
		return "", "", false, false
	}
	flags, _ := strconv.ParseUint(m[3], 16, 8)
	return m[1], m[2], flags&1 == 1, true
}

// StartSpan starts a span as a child of the span in ctx (or a new trace) and returns a context carrying it.
// Sampling is decided once per trace by Options.TracesRate. Safe to call without Init (the span is then a no-op).
func StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	var cfg spanCfg
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.kind == "" {
		cfg.kind = "internal"
	}
	sp := &Span{Name: clip(name, 200), Kind: cfg.kind, SpanID: randHex(8), tags: map[string]string{}, status: "ok", start: time.Now(),
		wall: time.Now().UTC().Format(time.RFC3339Nano)}
	for k, v := range cfg.tags {
		sp.tags[k] = v
	}
	if parent := SpanFromContext(ctx); parent != nil {
		sp.TraceID, sp.ParentID, sp.Sampled = parent.TraceID, parent.SpanID, parent.Sampled
	} else if tid, pid, smp, ok := ParseTraceParent(cfg.traceparent); ok {
		sp.TraceID, sp.ParentID, sp.Sampled = tid, pid, smp
	} else {
		sp.TraceID = randHex(16)
		s := get()
		sp.Sampled = s != nil && rand.Float64() < s.opts.TracesRate
	}
	return context.WithValue(ctx, spanKey{}, sp), sp
}

// SpanFromContext returns the active span or nil.
func SpanFromContext(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	sp, _ := ctx.Value(spanKey{}).(*Span)
	return sp
}

// SetTag adds a tag to the span.
func (sp *Span) SetTag(k, v string) {
	sp.mu.Lock()
	sp.tags[k] = v
	sp.mu.Unlock()
}

// Rename changes the span name (for example to the matched route once it is known).
func (sp *Span) Rename(name string) {
	sp.mu.Lock()
	sp.Name = clip(name, 200)
	sp.mu.Unlock()
}

// TraceParent returns the W3C traceparent value to send downstream.
func (sp *Span) TraceParent() string {
	f := "00"
	if sp.Sampled {
		f = "01"
	}
	return "00-" + sp.TraceID + "-" + sp.SpanID + "-" + f
}

// End finishes the span as ok.
func (sp *Span) End() { sp.EndStatus("ok") }

// EndErr finishes the span as "error" when err is non-nil.
func (sp *Span) EndErr(err error) {
	if err != nil {
		sp.EndStatus("error")
		return
	}
	sp.EndStatus("ok")
}

// EndStatus finishes the span with an explicit status ("ok" or "error").
func (sp *Span) EndStatus(status string) {
	defer func() { _ = recover() }()
	sp.mu.Lock()
	if sp.done {
		sp.mu.Unlock()
		return
	}
	sp.done = true
	tags := make(map[string]string, len(sp.tags))
	for k, v := range sp.tags {
		tags[k] = v
	}
	name := sp.Name
	sp.mu.Unlock()
	s := get()
	if s == nil || !sp.Sampled {
		return
	}
	s.enqueue(spansPath, map[string]any{"trace_id": sp.TraceID, "span_id": sp.SpanID, "parent_id": sp.ParentID, "name": name, "kind": sp.Kind,
		"start": sp.wall, "duration_ms": float64(time.Since(sp.start).Microseconds()) / 1000, "status": status,
		"tags": s.metricTags(tags), "release": s.opts.Release, "environment": s.opts.Environment})
}

// InjectHeaders adds `traceparent` for an outgoing request so the downstream service joins the trace in ctx.
func InjectHeaders(ctx context.Context, h http.Header) {
	if sp := SpanFromContext(ctx); sp != nil {
		h.Set("traceparent", sp.TraceParent())
	}
}

type tracingTransport struct{ base http.RoundTripper }

// NewTransport wraps an http.RoundTripper (nil = http.DefaultTransport): every outgoing request becomes a client span
// and carries a traceparent header. Use as `&http.Client{Transport: linearjeph.NewTransport(nil)}`.
func NewTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return tracingTransport{base}
}

func (t tracingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, sp := StartSpan(r.Context(), r.Method+" "+r.URL.Host, WithKind("client"), WithTags(map[string]string{"http.url": r.URL.Scheme + "://" + r.URL.Host + r.URL.Path}))
	r = r.Clone(ctx)
	InjectHeaders(ctx, r.Header)
	resp, err := t.base.RoundTrip(r)
	if resp != nil {
		sp.SetTag("http.status_code", itoa(resp.StatusCode))
	}
	if err != nil || (resp != nil && resp.StatusCode >= 500) {
		sp.EndStatus("error")
	} else {
		sp.End()
	}
	return resp, err
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	n, neg := len(b), i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}
