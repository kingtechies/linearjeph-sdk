// Package products is the LINEARJEPH product API client for Go: Fix, Shield, Ops, Models, Agents and Comply.
// It authenticates with an API key (ljk_...), sends JSON, retries 429 and 5xx on GET with backoff (honouring
// Retry-After) and returns a typed *ApiError for every non-2xx answer. Standard library only.
package products

import (
	"bytes"
	"context"
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
	"time"

	linearjeph "github.com/kingtechies/linearjeph-sdk/go"
)

// DefaultBaseURL is used when Options.BaseURL and LJ_BASE_URL are empty.
const DefaultBaseURL = "https://linearjeph.cc"

// JSON is a decoded JSON object. Lists come back under "results".
type JSON = map[string]any

// Query holds URL query parameters.
type Query map[string]string

// ErrTimeout is returned by the wait helpers when the deadline passes first.
var ErrTimeout = errors.New("linearjeph: timed out waiting")

// ApiError is every non-2xx answer, built from {"error": {"code", "message"}}.
type ApiError struct {
	Status  int
	Code    string
	Message string
	Body    JSON
}

func (e *ApiError) Error() string {
	return fmt.Sprintf("linearjeph: %d %s: %s", e.Status, e.Code, e.Message)
}

// Options configures a Client. Zero values select the defaults.
type Options struct {
	APIKey       string        // default $LJ_API_KEY
	BaseURL      string        // default $LJ_BASE_URL, then https://linearjeph.cc
	Org          string        // optional organization id, sent as ?org=
	Timeout      time.Duration // per request; default 30s
	Retries      int           // extra GET attempts on 429/5xx/network errors; default 2 (3 tries), negative = none
	Backoff      time.Duration // base backoff, doubles per attempt; default 500ms
	PollInterval time.Duration // wait helpers; default 3s
	HTTPClient   *http.Client  // optional custom client
}

// Client talks to the product API. Use the namespaces: Fix, Shield, Ops, Models, Agents, Comply.
type Client struct {
	o      Options
	base   string
	httpc  *http.Client
	Fix    *Fix
	Shield *Shield
	Ops    *Ops
	Models *Models
	Agents *Agents
	Comply *Comply
}

// New builds a client. It fails when no API key is available.
func New(o Options) (*Client, error) {
	if strings.TrimSpace(o.APIKey) == "" {
		o.APIKey = strings.TrimSpace(os.Getenv("LJ_API_KEY"))
	}
	if o.APIKey == "" {
		return nil, errors.New("linearjeph: Options.APIKey (or LJ_API_KEY) is required")
	}
	if o.BaseURL == "" {
		o.BaseURL = os.Getenv("LJ_BASE_URL")
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Retries == 0 {
		o.Retries = 2
	}
	if o.Retries < 0 {
		o.Retries = 0
	}
	if o.Backoff <= 0 {
		o.Backoff = 500 * time.Millisecond
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 3 * time.Second
	}
	c := &Client{o: o, base: strings.TrimRight(o.BaseURL, "/"), httpc: o.HTTPClient}
	if c.httpc == nil {
		c.httpc = &http.Client{}
	}
	c.Fix, c.Shield, c.Ops = &Fix{c}, &Shield{c}, &Ops{c}
	c.Models, c.Agents, c.Comply = &Models{c}, &Agents{c}, &Comply{c}
	return c, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, q Query) ([]byte, http.Header, error) {
	method = strings.ToUpper(method)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	vals := url.Values{}
	for k, v := range q {
		vals.Set(k, v)
	}
	if c.o.Org != "" && vals.Get("org") == "" {
		vals.Set("org", c.o.Org)
	}
	full := c.base + path
	if len(vals) > 0 {
		full += "?" + vals.Encode()
	}
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		payload = b
	}
	tries := 1
	if method == "GET" {
		tries += c.o.Retries
	}
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, c.delay(attempt, lastErr)); err != nil {
				return nil, nil, err
			}
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, full, rd)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.o.APIKey)
		req.Header.Set("User-Agent", "linearjeph-go/"+linearjeph.Version)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rctx, cancel := context.WithTimeout(ctx, c.o.Timeout)
		resp, err := c.httpc.Do(req.WithContext(rctx))
		if err != nil {
			cancel()
			lastErr = err
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			continue
		}
		data, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return data, resp.Header, nil
		}
		ae := apiError(resp.StatusCode, data)
		ae.retryAfter = resp.Header.Get("Retry-After")
		lastErr = ae
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			continue
		}
		return nil, resp.Header, ae.ApiError
	}
	if ae, ok := lastErr.(*retryErr); ok {
		return nil, nil, ae.ApiError
	}
	return nil, nil, lastErr
}

// retryErr carries Retry-After next to the public error.
type retryErr struct {
	*ApiError
	retryAfter string
}

func (r *retryErr) Error() string { return r.ApiError.Error() }

func apiError(status int, data []byte) *retryErr {
	e := &ApiError{Status: status, Code: "http_" + strconv.Itoa(status), Message: http.StatusText(status)}
	var b JSON
	if json.Unmarshal(data, &b) == nil && b != nil {
		e.Body = b
		if m, ok := b["error"].(map[string]any); ok {
			if s, ok := m["code"].(string); ok && s != "" {
				e.Code = s
			}
			if s, ok := m["message"].(string); ok && s != "" {
				e.Message = s
			}
		}
	}
	return &retryErr{ApiError: e}
}

func (c *Client) delay(attempt int, last error) time.Duration {
	if re, ok := last.(*retryErr); ok && re.retryAfter != "" {
		if s, err := strconv.ParseFloat(re.retryAfter, 64); err == nil && s >= 0 {
			if s > 30 {
				s = 30
			}
			return time.Duration(s * float64(time.Second))
		}
	}
	d := c.o.Backoff << (attempt - 1)
	return d + time.Duration(rand.Int63n(int64(d)/4+1))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Request calls any route. The answer is the decoded JSON object (an empty object for 204, {"data": ...} for a JSON
// list or scalar, {"raw": "..."} for non-JSON text). Paths are absolute, such as "/api/v1/fixops/overview".
func (c *Client) Request(ctx context.Context, method, path string, body any, q Query) (JSON, error) {
	data, hdr, err := c.do(ctx, method, path, body, q)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return JSON{}, nil
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		_ = hdr
		return JSON{"raw": string(data)}, nil
	}
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	return JSON{"data": v}, nil
}

// Download returns the raw response body, for patches and the compliance export zip.
func (c *Client) Download(ctx context.Context, path string, q Query) ([]byte, error) {
	data, _, err := c.do(ctx, "GET", path, nil, q)
	return data, err
}

// Whoami returns the organization, scopes and key details of the API key.
func (c *Client) Whoami(ctx context.Context) (JSON, error) {
	return c.Request(ctx, "GET", "/api/v1/apikeys/whoami", nil, nil)
}

func (c *Client) get(ctx context.Context, p string, q Query) (JSON, error) {
	return c.Request(ctx, "GET", p, nil, q)
}
func (c *Client) post(ctx context.Context, p string, body any) (JSON, error) {
	return c.Request(ctx, "POST", p, body, nil)
}

func idPath(prefix string, id int, rest ...string) string {
	p := prefix + "/" + strconv.Itoa(id)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// Results returns the "results" list of a list answer.
func Results(j JSON) []JSON {
	arr, _ := j["results"].([]any)
	out := make([]JSON, 0, len(arr))
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Str reads a string field ("" when missing).
func Str(j JSON, key string) string {
	switch v := j[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// Int reads a numeric id field (0 when missing).
func Int(j JSON, key string) int {
	switch v := j[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// pollUntil calls fn every interval until done reports true or the timeout passes.
func (c *Client) pollUntil(ctx context.Context, timeout time.Duration, fn func() (JSON, bool, error)) (JSON, error) {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		j, done, err := fn()
		if err != nil {
			return j, err
		}
		if done {
			return j, nil
		}
		if time.Now().Add(c.o.PollInterval).After(deadline) {
			return j, ErrTimeout
		}
		if err := sleep(ctx, c.o.PollInterval); err != nil {
			return j, err
		}
	}
}
