package linearjeph

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// HealthOptions sets thresholds for a numeric check. Higher is worse unless Below is set.
type HealthOptions struct {
	Warn    *float64
	Fail    *float64
	Below   bool          // lower values are worse (free disk space)
	Timeout time.Duration // default 5s
}

// Float returns a pointer to v, for HealthOptions thresholds: HealthOptions{Warn: linearjeph.Float(200)}.
func Float(v float64) *float64 { return &v }

// HealthResult is one check's outcome in the health endpoint response.
type HealthResult struct {
	Status  string   `json:"status"` // pass, warn or fail
	Value   *float64 `json:"value,omitempty"`
	Message string   `json:"message,omitempty"`
}

// HealthBody is the health endpoint response: status is ok, degraded (a warn) or fail (any fail).
type HealthBody struct {
	Status string                  `json:"status"`
	Checks map[string]HealthResult `json:"checks"`
}

type healthCheck struct {
	fn   func(context.Context) (float64, bool, error)
	opts HealthOptions
}

var health = struct {
	sync.RWMutex
	checks map[string]healthCheck
}{checks: map[string]healthCheck{}}

// RegisterHealthValue adds or replaces a check that returns a number (milliseconds, queue depth, free GB, ...) compared
// to the thresholds. A returned error is a failure.
func RegisterHealthValue(name string, fn func(context.Context) (float64, error), o HealthOptions) {
	health.Lock()
	defer health.Unlock()
	health.checks[clip(name, 60)] = healthCheck{fn: func(ctx context.Context) (float64, bool, error) { v, err := fn(ctx); return v, true, err }, opts: o}
}

// RegisterHealth adds or replaces a pass/fail check: a nil error is a pass.
func RegisterHealth(name string, fn func(context.Context) error, o HealthOptions) {
	health.Lock()
	defer health.Unlock()
	health.checks[clip(name, 60)] = healthCheck{fn: func(ctx context.Context) (float64, bool, error) { return 0, false, fn(ctx) }, opts: o}
}

// UnregisterHealth removes a check.
func UnregisterHealth(name string) {
	health.Lock()
	delete(health.checks, name)
	health.Unlock()
}

func grade(v float64, o HealthOptions) string {
	worse := func(lim float64) bool {
		if o.Below {
			return v <= lim
		}
		return v >= lim
	}
	if o.Fail != nil && worse(*o.Fail) {
		return "fail"
	}
	if o.Warn != nil && worse(*o.Warn) {
		return "warn"
	}
	return "pass"
}

func runOne(ctx context.Context, c healthCheck) (res HealthResult) {
	to := c.opts.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	type out struct {
		v   float64
		has bool
		err error
	}
	ch := make(chan out, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- out{err: fmt.Errorf("panic: %v", p)}
			}
		}()
		v, has, err := c.fn(ctx)
		ch <- out{v, has, err}
	}()
	select {
	case o := <-ch:
		if o.err != nil {
			return HealthResult{Status: "fail", Message: clip(o.err.Error(), 200)}
		}
		if o.has {
			v := o.v
			return HealthResult{Status: grade(v, c.opts), Value: &v}
		}
		return HealthResult{Status: "pass"}
	case <-ctx.Done():
		return HealthResult{Status: "fail", Message: "timed out after " + to.String()}
	}
}

// RunHealth runs every registered check in parallel, each with its own timeout.
func RunHealth(ctx context.Context) HealthBody {
	health.RLock()
	cs := make(map[string]healthCheck, len(health.checks))
	for k, v := range health.checks {
		cs[k] = v
	}
	health.RUnlock()
	body := HealthBody{Status: "ok", Checks: map[string]HealthResult{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, c := range cs {
		wg.Add(1)
		go func(name string, c healthCheck) {
			defer wg.Done()
			r := runOne(ctx, c)
			mu.Lock()
			body.Checks[name] = r
			mu.Unlock()
		}(name, c)
	}
	wg.Wait()
	for _, r := range body.Checks {
		if r.Status == "fail" {
			body.Status = "fail"
		} else if r.Status == "warn" && body.Status == "ok" {
			body.Status = "degraded"
		}
	}
	return body
}

// HealthHandler serves the health checks as JSON (200 when ok or degraded, 503 on any failure). With a non-empty token
// it requires `Authorization: Bearer <token>`. Mount it: `mux.Handle("/_lj/health", linearjeph.HealthHandler(token))`,
// then add an "SDK health check" monitor with that URL and token in the dashboard.
func HealthHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"unauthorized"}`))
			return
		}
		body := RunHealth(r.Context())
		if body.Status == "fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(body)
	})
}
