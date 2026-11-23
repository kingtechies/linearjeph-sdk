// Package prophecy is the LINEARJEPH Prophecy growth-intelligence SDK for Go. It tracks users, payments, features and
// API requests, scans a codebase locally into a manifest (never source code), imports history and fetches the
// projection report. Standard library only. Tracking calls never panic or return errors into the host application;
// delivery happens on a background goroutine (call Flush or Close before exit).
package prophecy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Version is the SDK version.
const Version = "1.3.0"

// DefaultEndpoint is used when Options.Endpoint is empty.
const DefaultEndpoint = "https://linearjeph.cc"

const (
	eventsPath   = "/ingest/v1/prophecy/events"
	identifyPath = "/ingest/v1/prophecy/identify"
	manifestPath = "/ingest/v1/prophecy/manifest"
	importPath   = "/api/v1/prophecy/sdk/import"
	reportPath   = "/api/v1/prophecy/sdk/report"
	refreshPath  = "/api/v1/prophecy/sdk/refresh"
	livePath     = "/api/v1/prophecy/sdk/live"
	embedPath    = "/api/v1/prophecy/sdk/embed"

	maxImportRows  = 5000
	maxImportBytes = 4500 * 1024
)

// Options configures a Client. Zero values select the documented defaults.
type Options struct {
	IngestKey     string        // application ingest key (write-only), sent as X-LJ-Key
	SecretKey     string        // project secret key (ljp_sk_...), sent as Authorization: Bearer; server side only
	Endpoint      string        // default https://linearjeph.cc
	HashUserIDs   *bool         // SHA-256 hex of every uid before it leaves the process; default true (see Bool)
	QueueSize     int           // bounded queue length per kind; default 10000 (full queue drops)
	BatchSize     int           // events per request (1..500); default 100
	FlushInterval time.Duration // background flush period; default 2s
	Retries       int           // retries after the first attempt on 429/5xx/network errors; default 4 (negative = none)
	Backoff       time.Duration // base backoff, doubles per attempt with jitter; default 500ms
	Timeout       time.Duration // per-request timeout; default 15s
	Debug         bool          // log SDK problems to stderr (otherwise each problem is logged once)
	HTTPClient    *http.Client  // optional custom client
}

var validKinds = map[string]bool{"signup": true, "login": true, "active": true, "feature": true, "payment": true,
	"refund": true, "subscription": true, "churn": true, "pageview": true, "request": true, "error": true, "custom": true}

// Bool returns a pointer to b, for Options.HashUserIDs.
func Bool(b bool) *bool { return &b }

// TrackOptions are the optional fields of Track.
type TrackOptions struct {
	UID      string
	Kind     string // signup|login|active|feature|payment|refund|subscription|churn|pageview|request|error|custom
	Feature  string
	Amount   float64
	Currency string
	Props    map[string]any
	TS       time.Time // default now
}

// SignupOptions are the optional fields of Signup.
type SignupOptions struct{ Plan, Channel, Country string }

// PaymentOptions are the optional fields of Payment.
type PaymentOptions struct {
	Currency string
	Plan     string
	Props    map[string]any
}

// ImportResult is the merged answer of ImportHistory.
type ImportResult struct {
	Accepted int
	Rejected int
	Errors   []string // first 20
	Jobs     []any
}

// Client is a Prophecy client. Create one with New, or use the package-level functions backed by Init.
type Client struct {
	o      Options
	hash   bool
	httpc  *http.Client
	mu     sync.Mutex
	events []map[string]any
	custs  []map[string]any
	wake   chan struct{}
	stop   chan struct{}
	done   chan struct{}
	flushM sync.Mutex
	closed atomic.Bool
	dropN  atomic.Int64
	warned sync.Map
}

// New validates the options and starts the background sender.
func New(o Options) (*Client, error) {
	if strings.TrimSpace(o.IngestKey) == "" && strings.TrimSpace(o.SecretKey) == "" {
		return nil, errors.New("prophecy: Options.IngestKey (or SecretKey) is required")
	}
	if o.Endpoint == "" {
		o.Endpoint = DefaultEndpoint
	}
	o.Endpoint = strings.TrimRight(o.Endpoint, "/")
	if o.QueueSize <= 0 {
		o.QueueSize = 10000
	}
	if o.BatchSize <= 0 || o.BatchSize > 500 {
		o.BatchSize = 100
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 2 * time.Second
	}
	if o.Retries == 0 {
		o.Retries = 4
	}
	if o.Retries < 0 {
		o.Retries = 0
	}
	if o.Backoff <= 0 {
		o.Backoff = 500 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Second
	}
	c := &Client{o: o, hash: o.HashUserIDs == nil || *o.HashUserIDs, httpc: o.HTTPClient,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	if c.httpc == nil {
		c.httpc = &http.Client{}
	}
	go c.loop()
	return c, nil
}

var (
	curMu sync.RWMutex
	cur   *Client
)

// Init configures the package-level client (the last call wins; the previous client is flushed and closed).
func Init(o Options) error {
	c, err := New(o)
	if err != nil {
		return err
	}
	curMu.Lock()
	old := cur
	cur = c
	curMu.Unlock()
	if old != nil {
		go old.Close(5 * time.Second)
	}
	return nil
}

func def() *Client {
	curMu.RLock()
	defer curMu.RUnlock()
	return cur
}

// Track, Feature, Signup, Payment, Subscription, Churn, Identify, Flush and Close on the package-level client. They do
// nothing before Init.
func Track(name string, o TrackOptions) {
	if c := def(); c != nil {
		c.Track(name, o)
	}
}
func Feature(name, uid string) {
	if c := def(); c != nil {
		c.Feature(name, uid)
	}
}
func Signup(uid string, o SignupOptions) {
	if c := def(); c != nil {
		c.Signup(uid, o)
	}
}
func Payment(uid string, amount float64, o PaymentOptions) {
	if c := def(); c != nil {
		c.Payment(uid, amount, o)
	}
}
func Subscription(uid, plan string, mrr float64, status string) {
	if c := def(); c != nil {
		c.Subscription(uid, plan, mrr, status)
	}
}
func Churn(uid, reason string) {
	if c := def(); c != nil {
		c.Churn(uid, reason)
	}
}
func Identify(uid string, traits map[string]any) {
	if c := def(); c != nil {
		c.Identify(uid, traits)
	}
}
func Flush(timeout time.Duration) bool {
	if c := def(); c != nil {
		return c.Flush(timeout)
	}
	return true
}
func Close(timeout time.Duration) bool {
	if c := def(); c != nil {
		return c.Close(timeout)
	}
	return true
}

// ---- internals ----------------------------------------------------------------------------------------------

func (c *Client) warn(key string, format string, a ...any) {
	if _, seen := c.warned.LoadOrStore(key, true); seen && !c.o.Debug {
		return
	}
	fmt.Fprintf(os.Stderr, "[prophecy] "+format+"\n", a...)
}

func (c *Client) guard() {
	if r := recover(); r != nil {
		c.warn("panic", "internal error ignored: %v", r)
	}
}

// HashUID returns the SHA-256 hex digest the SDK sends for a raw user id.
func HashUID(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:])
}

func (c *Client) uid(u string) string {
	if u == "" || !c.hash {
		return u
	}
	return HashUID(u)
}

func (c *Client) enqueueEvent(e map[string]any) {
	c.mu.Lock()
	if c.closed.Load() || len(c.events) >= c.o.QueueSize {
		c.mu.Unlock()
		c.dropN.Add(1)
		return
	}
	c.events = append(c.events, e)
	n := len(c.events)
	c.mu.Unlock()
	if n >= c.o.BatchSize {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

func (c *Client) enqueueCustomer(m map[string]any) {
	c.mu.Lock()
	if c.closed.Load() || len(c.custs) >= c.o.QueueSize {
		c.mu.Unlock()
		c.dropN.Add(1)
		return
	}
	c.custs = append(c.custs, m)
	c.mu.Unlock()
}

// Dropped returns how many events were dropped because the queue was full or the client closed.
func (c *Client) Dropped() int64 { return c.dropN.Load() }

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Track queues one event. It never panics and never returns an error.
func (c *Client) Track(name string, o TrackOptions) {
	defer c.guard()
	if c == nil || strings.TrimSpace(name) == "" {
		return
	}
	if len(name) > 80 {
		name = name[:80]
	}
	kind := o.Kind
	if kind == "" {
		if o.Feature != "" {
			kind = "feature"
		} else {
			kind = "custom"
		}
	}
	if !validKinds[kind] {
		kind = "custom"
	}
	ts := o.TS
	if ts.IsZero() {
		ts = time.Now()
	}
	e := map[string]any{"ts": iso(ts), "name": name, "kind": kind}
	if u := c.uid(o.UID); u != "" {
		e["uid"] = u
	}
	if o.Feature != "" {
		e["feature"] = o.Feature
	}
	if o.Amount != 0 || kind == "payment" {
		e["amount"] = o.Amount
	}
	if o.Currency != "" {
		cur := strings.ToUpper(o.Currency)
		if len(cur) > 3 {
			cur = cur[:3]
		}
		e["currency"] = cur
	}
	if len(o.Props) > 0 {
		if raw, err := json.Marshal(o.Props); err != nil || len(raw) > 2048 {
			c.warn("props", "props dropped (not serialisable or over 2 KB)")
		} else {
			e["props"] = o.Props
		}
	}
	c.enqueueEvent(e)
}

// Feature records one use of a product feature.
func (c *Client) Feature(name, uid string) {
	defer c.guard()
	c.Track(name, TrackOptions{UID: uid, Kind: "feature", Feature: name})
}

// Signup records a signup and creates the customer.
func (c *Client) Signup(uid string, o SignupOptions) {
	defer c.guard()
	props := map[string]any{}
	cust := map[string]any{"signup_at": iso(time.Now())}
	for k, v := range map[string]string{"plan": o.Plan, "channel": o.Channel, "country": o.Country} {
		if v != "" {
			props[k] = v
			cust[k] = v
		}
	}
	c.Track("signup", TrackOptions{UID: uid, Kind: "signup", Props: props})
	c.identifyMap(uid, cust)
}

// Payment records a successful payment (use a negative amount for a refund).
func (c *Client) Payment(uid string, amount float64, o PaymentOptions) {
	defer c.guard()
	kind := "payment"
	if amount < 0 {
		kind = "refund"
	}
	props := map[string]any{}
	for k, v := range o.Props {
		props[k] = v
	}
	if o.Plan != "" {
		props["plan"] = o.Plan
	}
	cur := o.Currency
	if cur == "" {
		cur = "USD"
	}
	c.Track(kind, TrackOptions{UID: uid, Kind: kind, Amount: amount, Currency: cur, Props: props})
}

// Subscription records a subscription state (status: active, cancelled or paused) and its MRR.
func (c *Client) Subscription(uid, plan string, mrr float64, status string) {
	defer c.guard()
	if status == "" {
		status = "active"
	}
	c.Track("subscription", TrackOptions{UID: uid, Kind: "subscription",
		Props: map[string]any{"plan": plan, "mrr": mrr, "status": status}})
	c.identifyMap(uid, map[string]any{"plan": plan, "mrr": mrr})
}

// Churn records that a customer left.
func (c *Client) Churn(uid, reason string) {
	defer c.guard()
	var p map[string]any
	if reason != "" {
		p = map[string]any{"reason": reason}
	}
	c.Track("churn", TrackOptions{UID: uid, Kind: "churn", Props: p})
}

// Identify attaches traits to a customer. plan, mrr, country, channel and signup_at become customer fields; the rest
// (at most 20 keys) are traits.
func (c *Client) Identify(uid string, traits map[string]any) {
	defer c.guard()
	c.identifyMap(uid, traits)
}

func (c *Client) identifyMap(uid string, traits map[string]any) {
	u := c.uid(uid)
	if u == "" {
		return
	}
	m := map[string]any{"uid": u}
	rest := map[string]any{}
	for k, v := range traits {
		switch k {
		case "plan", "mrr", "country", "channel", "signup_at":
			m[k] = v
		default:
			if len(rest) < 20 {
				rest[k] = v
			}
		}
	}
	if len(rest) > 0 {
		m["traits"] = rest
	}
	c.enqueueCustomer(m)
}

// RecordRequest records one handled API request (route template, method, status, duration). Use it from any router.
func (c *Client) RecordRequest(route, method string, status int, dur time.Duration) {
	defer c.guard()
	if c == nil {
		return
	}
	c.Track("request", TrackOptions{Kind: "request", Props: map[string]any{
		"route": route, "method": strings.ToUpper(method), "status": status,
		"duration_ms": float64(dur.Microseconds()) / 1000,
	}})
}

// RecordRequest records a request on the package-level client.
func RecordRequest(route, method string, status int, dur time.Duration) {
	if c := def(); c != nil {
		c.RecordRequest(route, method, status, dur)
	}
}

func (c *Client) loop() {
	defer close(c.done)
	t := time.NewTicker(c.o.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.drain(context.Background())
		case <-c.wake:
			c.drain(context.Background())
		}
	}
}

func (c *Client) take() (ev, cu []map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.o.BatchSize
	if len(c.events) < n {
		n = len(c.events)
	}
	ev = c.events[:n:n]
	c.events = c.events[n:]
	m := c.o.BatchSize
	if len(c.custs) < m {
		m = len(c.custs)
	}
	cu = c.custs[:m:m]
	c.custs = c.custs[m:]
	return
}

func (c *Client) drain(ctx context.Context) {
	defer c.guard()
	c.flushM.Lock()
	defer c.flushM.Unlock()
	for ctx.Err() == nil {
		ev, cu := c.take()
		if len(ev) == 0 && len(cu) == 0 {
			return
		}
		if len(cu) > 0 {
			c.send(ctx, identifyPath, map[string]any{"customers": cu})
		}
		if len(ev) > 0 {
			c.send(ctx, eventsPath, map[string]any{"events": ev})
		}
	}
}

func (c *Client) send(ctx context.Context, path string, body any) {
	if c.o.IngestKey == "" {
		c.warn("nokey", "no IngestKey configured: events are discarded")
		return
	}
	_, _, err := c.do(ctx, "POST", path, body, false)
	if err != nil {
		c.warn("send", "delivery to %s failed: %v", path, err)
	}
}

// Flush sends everything queued, waiting up to timeout. It reports whether the queue was fully delivered.
func (c *Client) Flush(timeout time.Duration) bool {
	defer c.guard()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c.drain(ctx)
	c.mu.Lock()
	left := len(c.events) + len(c.custs)
	c.mu.Unlock()
	return left == 0
}

// Close flushes and stops the background goroutine. Later tracking calls are dropped.
func (c *Client) Close(timeout time.Duration) bool {
	ok := c.Flush(timeout)
	if c.closed.CompareAndSwap(false, true) {
		close(c.stop)
		<-c.done
	}
	return ok
}

// do performs a request with gzip and retries. secret selects Authorization: Bearer instead of X-LJ-Key.
func (c *Client) do(ctx context.Context, method, path string, body any, secret bool) (int, []byte, error) {
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(raw)
		zw.Close()
		payload = buf.Bytes()
	}
	var lastErr error
	for attempt := 0; attempt <= c.o.Retries; attempt++ {
		if attempt > 0 {
			wait := c.o.Backoff << (attempt - 1)
			wait += time.Duration(rand.Int63n(int64(wait)/2 + 1))
			if lastErr != nil {
				var re *httpError
				if errors.As(lastErr, &re) && re.retryAfter > 0 && re.retryAfter < 30*time.Second {
					wait = re.retryAfter
				}
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
		}
		rctx, cancel := context.WithTimeout(ctx, c.o.Timeout)
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(rctx, method, c.o.Endpoint+path, rd)
		if err != nil {
			cancel()
			return 0, nil, err
		}
		req.Header.Set("User-Agent", "linearjeph-prophecy-go/"+Version)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "gzip")
		}
		if secret {
			req.Header.Set("Authorization", "Bearer "+c.o.SecretKey)
		} else {
			req.Header.Set("X-LJ-Key", c.o.IngestKey)
		}
		resp, err := c.httpc.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		cancel()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			ra := time.Duration(0)
			if s, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil {
				ra = time.Duration(s) * time.Second
			}
			lastErr = &httpError{status: resp.StatusCode, body: trim(data), retryAfter: ra}
			continue
		}
		if resp.StatusCode >= 400 {
			return resp.StatusCode, data, &httpError{status: resp.StatusCode, body: trim(data)}
		}
		return resp.StatusCode, data, nil
	}
	return 0, nil, lastErr
}

type httpError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d %s", e.status, e.body) }

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ---- manifest -----------------------------------------------------------------------------------------------

// SendManifest uploads a manifest produced by Scan (ingest key). Unlike tracking calls it returns the error.
func (c *Client) SendManifest(m Manifest) error {
	if c.o.IngestKey == "" {
		return errors.New("prophecy: IngestKey is required to send a manifest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _, err := c.do(ctx, "POST", manifestPath, map[string]any{"manifest": m}, false)
	return err
}

// SendManifest uploads a manifest with the package-level client.
func SendManifest(m Manifest) error {
	c := def()
	if c == nil {
		return errors.New("prophecy: call Init first")
	}
	return c.SendManifest(m)
}

// ---- secret-key API (server side only) ----------------------------------------------------------------------

func (c *Client) secret(method, path string, body any) (map[string]any, error) {
	if c.o.SecretKey == "" {
		return nil, errors.New("prophecy: Options.SecretKey is required for this call")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, data, err := c.do(ctx, method, path, body, true)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Report returns the latest report ({"report", "created_at", "stale"}); a section name (for example "revenue")
// narrows it to one section.
func (c *Client) Report(section string) (map[string]any, error) {
	p := reportPath
	if section != "" {
		p += "?section=" + url.QueryEscape(section)
	}
	return c.secret("GET", p, nil)
}

// Refresh queues a new report build.
func (c *Client) Refresh() (map[string]any, error) {
	return c.secret("POST", refreshPath, map[string]any{})
}

// Live returns the realtime counters.
func (c *Client) Live() (map[string]any, error) { return c.secret("GET", livePath, nil) }

// EmbedURL returns a signed, chromeless report URL valid for ttlSeconds (max 86400) for the owner's admin panel.
func (c *Client) EmbedURL(ttlSeconds int) (string, error) {
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	out, err := c.secret("POST", embedPath, map[string]any{"ttl": ttlSeconds})
	if err != nil {
		return "", err
	}
	u, _ := out["url"].(string)
	if u == "" {
		return "", errors.New("prophecy: no url in response")
	}
	return u, nil
}

// ImportHistory backfills history. kind is "events" or "customers"; rows use the ingest row shape (an optional "id"
// dedupes). Rows are sent in batches of at most 5,000 rows and 5 MB, retried on 429/5xx. progress (optional) is called
// after each batch with rows done and total. uids are hashed like live events unless HashUserIDs is false.
func (c *Client) ImportHistory(kind string, rows []map[string]any, progress func(done, total int)) (ImportResult, error) {
	var res ImportResult
	if kind != "events" && kind != "customers" {
		return res, errors.New(`prophecy: kind must be "events" or "customers"`)
	}
	if c.o.SecretKey == "" {
		return res, errors.New("prophecy: Options.SecretKey is required for ImportHistory")
	}
	prepared := make([]map[string]any, len(rows))
	for i, r := range rows {
		cp := make(map[string]any, len(r))
		for k, v := range r {
			cp[k] = v
		}
		if u, ok := cp["uid"].(string); ok && u != "" {
			cp["uid"] = c.uid(u)
		}
		prepared[i] = cp
	}
	done := 0
	var post func(batch []map[string]any) error
	post = func(batch []map[string]any) error {
		raw, _ := json.Marshal(batch)
		if len(batch) > 1 && len(raw) > maxImportBytes {
			h := len(batch) / 2
			if err := post(batch[:h]); err != nil {
				return err
			}
			return post(batch[h:])
		}
		out, err := c.secret("POST", importPath, map[string]any{"kind": kind, "rows": batch})
		if err != nil {
			return err
		}
		res.Accepted += toInt(out["accepted"])
		res.Rejected += toInt(out["rejected"])
		if j, ok := out["job"]; ok {
			res.Jobs = append(res.Jobs, j)
		}
		if es, ok := out["errors"].([]any); ok {
			for _, e := range es {
				if len(res.Errors) < 20 {
					res.Errors = append(res.Errors, fmt.Sprint(e))
				}
			}
		}
		done += len(batch)
		if progress != nil {
			func() {
				defer c.guard()
				progress(done, len(prepared))
			}()
		}
		return nil
	}
	for i := 0; i < len(prepared); i += maxImportRows {
		j := i + maxImportRows
		if j > len(prepared) {
			j = len(prepared)
		}
		if err := post(prepared[i:j]); err != nil {
			return res, err
		}
	}
	return res, nil
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// Report, Refresh, Live, EmbedURL and ImportHistory on the package-level client.
func Report(section string) (map[string]any, error) {
	c := def()
	if c == nil {
		return nil, errors.New("prophecy: call Init first")
	}
	return c.Report(section)
}
func Refresh() (map[string]any, error) {
	c := def()
	if c == nil {
		return nil, errors.New("prophecy: call Init first")
	}
	return c.Refresh()
}
func Live() (map[string]any, error) {
	c := def()
	if c == nil {
		return nil, errors.New("prophecy: call Init first")
	}
	return c.Live()
}
func EmbedURL(ttlSeconds int) (string, error) {
	c := def()
	if c == nil {
		return "", errors.New("prophecy: call Init first")
	}
	return c.EmbedURL(ttlSeconds)
}
func ImportHistory(kind string, rows []map[string]any, progress func(done, total int)) (ImportResult, error) {
	c := def()
	if c == nil {
		return ImportResult{}, errors.New("prophecy: call Init first")
	}
	return c.ImportHistory(kind, rows, progress)
}
