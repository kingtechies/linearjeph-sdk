package prophecy

import (
	"bufio"
	"net"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var (
	reNum  = regexp.MustCompile(`^\d+$`)
	reUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	reHex  = regexp.MustCompile(`(?i)^[0-9a-f]{16,}$`)
)

// NormalizePath turns a raw URL path into a route template: numeric, UUID and long hex segments become ":id".
func NormalizePath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s != "" && (reNum.MatchString(s) || reUUID.MatchString(s) || reHex.MatchString(s)) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}

// patternOf reads http.Request.Pattern (Go 1.23+) by reflection so this package still builds on older toolchains.
func patternOf(r *http.Request) string {
	f := reflect.ValueOf(r).Elem().FieldByName("Pattern")
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

// routeOf returns the route template for a handled request.
func routeOf(r *http.Request) string {
	p := patternOf(r)
	if p == "" {
		return NormalizePath(r.URL.Path)
	}
	return templateFromPattern(p)
}

// templateFromPattern converts a ServeMux pattern ("GET /users/{id}") into a route template ("/users/:id").
func templateFromPattern(p string) string {
	if i := strings.Index(p, " "); i >= 0 { // "GET /users/{id}"
		p = p[i+1:]
	}
	if i := strings.Index(p, "/"); i > 0 { // host-qualified pattern
		p = p[i:]
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] != '{' {
			b.WriteByte(p[i])
			continue
		}
		j := strings.IndexByte(p[i:], '}')
		if j < 0 {
			b.WriteString(p[i:])
			break
		}
		name := strings.TrimSuffix(p[i+1:i+j], "...")
		if name == "$" {
			b.WriteString("{$}")
		} else {
			b.WriteString(":" + name)
		}
		i += j
	}
	return b.String()
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Middleware times every request and records route template, method, status and duration (never bodies or query
// strings) on the package-level client (looked up per request, so it may be installed before Init). A handler panic is
// recorded as 500 and re-raised.
func Middleware(next http.Handler) http.Handler { return middleware(def, next) }

// Middleware is Middleware bound to this client.
func (c *Client) Middleware(next http.Handler) http.Handler {
	return middleware(func() *Client { return c }, next)
}

func middleware(get func() *Client, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := get()
		if c == nil {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			rec := recover()
			if rec != nil {
				status = 500
			} else if status == 0 {
				status = 200
			}
			c.RecordRequest(routeOf(r), r.Method, status, time.Since(start))
			if rec != nil {
				panic(rec)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}
