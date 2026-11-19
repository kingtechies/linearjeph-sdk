package linearjeph

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- PII scrubbing

var (
	piiEmail    = regexp.MustCompile(`[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63})+`)
	piiCard     = regexp.MustCompile(`(?:\d[ -]?){12,18}\d`)
	piiJWT      = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	piiPrefixed = regexp.MustCompile(`\b(?:lj_[a-z]{2,5}_[A-Za-z0-9_-]{8,}|ljp_[A-Za-z0-9_-]{8,}|ljr_[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,})\b`)
	metricName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:/-]{0,99}$`)
)

func luhn(d string) bool {
	sum, alt := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

func isDigitOrDash(b byte) bool { return (b >= '0' && b <= '9') || b == '-' }

// ScrubPII replaces emails, payment card numbers (Luhn-valid), JWTs, API keys and bearer tokens, plus
// `password=...` style text. The SDK applies it to log messages, log context and tag values unless Options.NoPIIScrub.
func ScrubPII(text string) string {
	t := scrubText(text)
	t = piiJWT.ReplaceAllString(t, "[token]")
	t = piiPrefixed.ReplaceAllString(t, "[token]")
	t = piiEmail.ReplaceAllString(t, "[email]")
	var out strings.Builder
	last := 0
	for _, m := range piiCard.FindAllStringIndex(t, -1) {
		a, b := m[0], m[1]
		if (a > 0 && isDigitOrDash(t[a-1])) || (b < len(t) && isDigitOrDash(t[b])) {
			continue
		}
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, t[a:b])
		if len(digits) >= 13 && len(digits) <= 19 && luhn(digits) {
			out.WriteString(t[last:a])
			out.WriteString("[card]")
			last = b
		}
	}
	out.WriteString(t[last:])
	return out.String()
}

func (s *sdk) clean(v string, n int) string {
	if len(v) > n*2 {
		v = v[:n*2]
	}
	if !s.opts.NoPIIScrub {
		v = ScrubPII(v)
	}
	return clip(v, n)
}

func (s *sdk) metricTags(in map[string]string) map[string]string {
	out := map[string]string{}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out) >= 10 {
			break
		}
		if sensitive(s, k) {
			out[clip(k, 50)] = filtered
		} else {
			out[clip(k, 50)] = s.clean(in[k], 100)
		}
	}
	return out
}

// ---------------------------------------------------------------- metrics

const reservoir = 100

type series struct {
	typ                 string
	name                string
	tags                map[string]string
	count               int64
	sum, min, max, last float64
	values              []float64
}

type metricStore struct {
	mu    sync.Mutex
	m     map[string]*series
	drops int64
}

func newMetricStore() *metricStore { return &metricStore{m: map[string]*series{}} }

func (s *sdk) record(typ, name string, value float64, tags map[string]string) {
	if s.off.Load() || s.closed.Load() || !metricName.MatchString(name) || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	t := s.metricTags(tags)
	var kb strings.Builder
	kb.WriteString(typ + "\x00" + name)
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kb.WriteString("\x00" + k + "=" + t[k])
	}
	m := s.metrics
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.m[kb.String()]
	if a == nil {
		if len(m.m) >= s.opts.MaxMetricSeries {
			m.drops++ // bounded memory: new series beyond the cap are dropped and counted
			return
		}
		a = &series{typ: typ, name: name, tags: t, min: value, max: value}
		m.m[kb.String()] = a
	}
	a.count++
	a.sum += value
	a.last = value
	a.min, a.max = math.Min(a.min, value), math.Max(a.max, value)
	if typ == "histogram" {
		if len(a.values) < reservoir {
			a.values = append(a.values, value)
		} else if j := rand.Int63n(a.count); j < reservoir {
			a.values[j] = value
		}
	}
}

// Incr adds value to a counter (sent as one summed point per flush interval).
func Incr(name string, value float64, tags map[string]string) {
	defer func() { _ = recover() }()
	if s := get(); s != nil {
		s.record("counter", name, value, tags)
	}
}

// Gauge records the current value of something (the latest value in each interval wins).
func Gauge(name string, value float64, tags map[string]string) {
	defer func() { _ = recover() }()
	if s := get(); s != nil {
		s.record("gauge", name, value, tags)
	}
}

// Histogram records one observation: count, sum, min, max and a bounded sample for p50/p95/p99.
func Histogram(name string, value float64, tags map[string]string) {
	defer func() { _ = recover() }()
	if s := get(); s != nil {
		s.record("histogram", name, value, tags)
	}
}

// Timer measures a duration into a histogram in milliseconds: `defer linearjeph.StartTimer("db.query", nil).Stop()`.
type Timer struct {
	name  string
	tags  map[string]string
	start time.Time
	once  sync.Once
}

// StartTimer starts a Timer.
func StartTimer(name string, tags map[string]string) *Timer {
	return &Timer{name: name, tags: tags, start: time.Now()}
}

// Stop records the elapsed milliseconds (once).
func (t *Timer) Stop() {
	t.once.Do(func() { Histogram(t.name, float64(time.Since(t.start).Microseconds())/1000, t.tags) })
}

// Time runs fn and records its duration in milliseconds.
func Time(name string, tags map[string]string, fn func()) {
	t := StartTimer(name, tags)
	defer t.Stop()
	fn()
}

// DroppedMetricSeries returns how many new series were refused because MaxMetricSeries was reached.
func DroppedMetricSeries() int64 {
	if s := get(); s != nil {
		s.metrics.mu.Lock()
		defer s.metrics.mu.Unlock()
		return s.metrics.drops
	}
	return 0
}

func (s *sdk) flushMetrics() int {
	m := s.metrics
	m.mu.Lock()
	snap := m.m
	m.m = map[string]*series{}
	m.mu.Unlock()
	if len(snap) == 0 {
		return 0
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	items := make([]any, 0, len(snap))
	for _, a := range snap {
		it := map[string]any{"name": a.name, "type": a.typ, "tags": a.tags, "timestamp": ts}
		switch a.typ {
		case "counter":
			it["value"] = a.sum
		case "gauge":
			it["value"] = a.last
		default:
			it["count"], it["sum"], it["min"], it["max"], it["values"] = a.count, a.sum, a.min, a.max, a.values
		}
		items = append(items, it)
	}
	for i := 0; i < len(items); i += 200 {
		s.enqueue(metricsPath, map[string]any{"metrics": items[i:min(i+200, len(items))]})
	}
	return len(items)
}

func (s *sdk) metricLoop() {
	t := time.NewTicker(s.opts.MetricsInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.flushMetrics()
		case <-s.stop:
			return
		}
	}
}

// ---------------------------------------------------------------- structured logs

// LogItem is one log line as sent to the ingest. BeforeSendLog receives and may modify it.
type LogItem struct {
	Timestamp   string         `json:"timestamp"`
	Level       string         `json:"level"`
	Message     string         `json:"message"`
	Logger      string         `json:"logger"`
	Context     map[string]any `json:"context"`
	Release     string         `json:"release"`
	Environment string         `json:"environment"`
	TraceID     string         `json:"trace_id,omitempty"`
	SpanID      string         `json:"span_id,omitempty"`
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError+4:
		return "critical"
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warning"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}

func (s *sdk) sendLog(ctx context.Context, level slog.Level, msg, logger string, attrs map[string]any) {
	if level < s.opts.LogLevel {
		return
	}
	if s.opts.LogSampleRate < 1 && level < slog.LevelError && rand.Float64() >= s.opts.LogSampleRate {
		return
	}
	cx := map[string]any{}
	n := 0
	for k, v := range attrs {
		if n++; n > 30 {
			break
		}
		k = clip(k, 50)
		switch x := v.(type) {
		case int, int64, float64, bool, uint64, int32:
			cx[k] = x
		default:
			if sensitive(s, k) {
				cx[k] = filtered
			} else {
				cx[k] = s.clean(fmt.Sprint(v), 300)
			}
		}
		if sensitive(s, k) {
			cx[k] = filtered
		}
	}
	it := &LogItem{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Level: levelName(level), Message: s.clean(msg, 4000),
		Logger: clip(logger, 100), Context: cx, Release: s.opts.Release, Environment: s.opts.Environment}
	if sp := SpanFromContext(ctx); sp != nil {
		it.TraceID, it.SpanID = sp.TraceID, sp.SpanID
	}
	if f := s.opts.BeforeSendLog; f != nil {
		if it = f(*it); it == nil {
			return
		}
	}
	s.enqueue(logsPath, it)
}

// Log sends one structured log line at the given level. Fields become searchable context; the active span (if
// ctx carries one) links the line to its trace. Never panics.
func Log(ctx context.Context, level slog.Level, msg string, fields map[string]any) {
	defer func() { _ = recover() }()
	if s := get(); s != nil {
		s.sendLog(ctx, level, msg, "", fields)
	}
}

// SlogHandler is a log/slog Handler: `slog.SetDefault(slog.New(linearjeph.NewSlogHandler(nil)))`. Pass a next handler
// to keep logging to stdout as well; it always receives the record, whatever LINEARJEPH does with it.
type SlogHandler struct {
	next   slog.Handler
	attrs  []slog.Attr
	groups []string
}

// NewSlogHandler wraps next (may be nil). Level filtering for LINEARJEPH is Options.LogLevel.
func NewSlogHandler(next slog.Handler) *SlogHandler { return &SlogHandler{next: next} }

// Enabled implements slog.Handler.
func (h *SlogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	if h.next != nil && h.next.Enabled(ctx, l) {
		return true
	}
	s := get()
	return s != nil && l >= s.opts.LogLevel
}

// Handle implements slog.Handler.
func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	func() {
		defer func() { _ = recover() }()
		if s := get(); s != nil {
			f := map[string]any{}
			prefix := strings.Join(h.groups, ".")
			put := func(a slog.Attr) {
				k := a.Key
				if prefix != "" {
					k = prefix + "." + k
				}
				f[k] = a.Value.Resolve().Any()
			}
			for _, a := range h.attrs {
				put(a)
			}
			r.Attrs(func(a slog.Attr) bool { put(a); return true })
			s.sendLog(ctx, r.Level, r.Message, "slog", f)
		}
	}()
	if h.next != nil && h.next.Enabled(ctx, r.Level) {
		return h.next.Handle(ctx, r)
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (h *SlogHandler) WithAttrs(a []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), a...)
	if h.next != nil {
		c.next = h.next.WithAttrs(a)
	}
	return &c
}

// WithGroup implements slog.Handler.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.groups = append(append([]string{}, h.groups...), name)
	if h.next != nil {
		c.next = h.next.WithGroup(name)
	}
	return &c
}
