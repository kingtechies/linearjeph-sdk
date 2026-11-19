package prophecy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	root := filepath.Join(t.TempDir(), "shop")
	write(t, root, "go.mod", "module x\n\ngo 1.21\n\nrequire (\n\tgithub.com/go-chi/chi/v5 v5.0.10\n\tgithub.com/stripe/stripe-go/v76 v76.1.0\n\tgolang.org/x/sys v0.1.0 // indirect\n)\n")
	write(t, root, "go.sum", "x")
	write(t, root, "main.go", `package main
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h)
	http.HandleFunc("/legacy", h)
	r := chi.NewRouter()
	r.Get("/api/v1/orders/{id}", h)
	api := r.Group("/api")
	api.Post("/invoices", h)
	g := gin.Default()
	g.GET("/ping", h)
	e.DELETE("/users/:id", h)
	secret := os.Getenv("STRIPE_SECRET_KEY")
	_ = os.Getenv("FEATURE_DARK_MODE")
	if flags.IsEnabled("new-checkout") {}
}
`)
	write(t, root, "main_test.go", "package main\nfunc TestX(t *testing.T){}\n")
	write(t, root, "app/Http/routes.php", "<?php\nRoute::get('/posts/{post}', f);\nRoute::resource('photos', P::class);\n")
	write(t, root, "config/routes.rb", "Rails.application.routes.draw do\n  namespace :admin do\n    resources :users\n    get '/stats', to: 'x#y'\n  end\n  post '/login', to: 's#c'\nend\n")
	write(t, root, "web/server.js", "app.get('/express/items', h)\nconst k = process.env.SENTRY_DSN\n")
	write(t, root, "app/dashboard/page.tsx", "export default 1\n")
	write(t, root, "pyapp/urls.py", "urlpatterns = [path('articles/<int:pk>/', v), path('', v)]\n")
	write(t, root, "package.json", `{"dependencies":{"express":"^4.18.0","react":"18.2.0"},"devDependencies":{"jest":"29.0.0"}}`)
	write(t, root, "composer.json", `{"require":{"php":">=8.1","laravel/framework":"^10.0","ext-json":"*"}}`)
	write(t, root, "Gemfile.lock", "GEM\n  remote: https://rubygems.org/\n  specs:\n    rails (7.1.0)\n      actioncable (= 7.1.0)\n    rspec (3.12.0)\n\nPLATFORMS\n  ruby\n")
	write(t, root, "requirements.txt", "# c\nDjango==4.2.1\nrequests>=2.0\n-r other.txt\n")
	write(t, root, "pyproject.toml", "[project]\ndependencies = [\n  \"fastapi>=0.100\",\n  \"pydantic\",\n]\n")
	write(t, root, "README.md", "# Shop\n\nA [store](http://x) for **things**.\n"+strings.Repeat("filler ", 600))
	write(t, root, ".env", "DATABASE_URL=postgres://user:supersecretvalue@db/x\nPAYSTACK_KEY=sk_test_abc\n")
	write(t, root, ".env.example", "REDIS_URL=\nexport MIXPANEL_TOKEN=\n")
	write(t, root, "Dockerfile", "FROM scratch\n")
	write(t, root, "docker-compose.yml", "services: {}\n")
	write(t, root, ".github/workflows/ci.yml", "on: push\n")
	write(t, root, "vercel.json", "{}")
	write(t, root, "billing/plans.json", `{"plans":[{"name":"starter","price":29,"currency":"USD"},{"name":"pro","price":99}]}`)
	write(t, root, "billing/keys.go", "package billing\nvar k = \"AKIA" + "ABCDEFGHIJKLMNOP\"\nvar s = \"sk_" + "live_abcdefghijklmnopqrstuv\"\n")
	// must be skipped
	write(t, root, "node_modules/leak/index.js", "app.get('/leak', h)\n")
	write(t, root, "vendor/x/y.go", "package y\n")
	write(t, root, ".git/config", "x")
	write(t, root, "big.js", "// "+strings.Repeat("a", 1100*1024))
	// symlink escaping root
	outside := t.TempDir()
	write(t, outside, "evil.js", "app.get('/escaped', h)\n")
	_ = os.Symlink(outside, filepath.Join(root, "link"))
	_ = os.Symlink(filepath.Join(outside, "evil.js"), filepath.Join(root, "evil-link.js"))
	return root
}

func TestScanFixture(t *testing.T) {
	root := fixture(t)
	m, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != 1 || m.RootName != "shop" || m.ScannedAt == "" {
		t.Errorf("header %+v", m)
	}
	if m.Languages["go"] != 3 || m.Languages["js"] != 1 || m.Languages["php"] != 1 || m.LOC < 20 {
		t.Errorf("languages %v loc %d", m.Languages, m.LOC)
	}
	routes := map[string]bool{}
	for _, r := range m.Routes {
		routes[r.Method+" "+r.Path] = true
		if strings.Contains(r.Path, "leak") || strings.Contains(r.Path, "escaped") {
			t.Errorf("scanned excluded file: %v", r)
		}
	}
	for _, want := range []string{"GET /health", "ANY /legacy", "GET /api/v1/orders/:id", "POST /api/invoices", "GET /ping",
		"DELETE /users/:id", "GET /posts/:post", "GET /photos", "DELETE /photos/:id", "GET /admin/users", "GET /admin/stats",
		"POST /login", "GET /express/items", "GET /dashboard", "ANY /articles/:pk/", "ANY /"} {
		if !routes[want] {
			t.Errorf("missing route %q in %v", want, routes)
		}
	}
	deps := map[string]string{}
	for _, d := range m.Dependencies {
		deps[d.Ecosystem+":"+d.Name] = d.Version
	}
	for k, v := range map[string]string{"go:github.com/go-chi/chi/v5": "v5.0.10", "npm:express": "^4.18.0", "npm:jest": "29.0.0",
		"composer:laravel/framework": "^10.0", "gem:rails": "7.1.0", "pypi:django": "4.2.1", "pypi:requests": ">=2.0", "pypi:fastapi": ">=0.100", "pypi:pydantic": ""} {
		if got, ok := deps[k]; !ok || got != v {
			t.Errorf("dep %s = %q (present %v), want %q", k, got, ok, v)
		}
	}
	if _, ok := deps["go:golang.org/x/sys"]; ok {
		t.Error("indirect dep included")
	}
	if _, ok := deps["composer:php"]; ok {
		t.Error("php constraint included")
	}
	for _, f := range []string{"chi", "express", "react", "laravel", "rails", "fastapi", "django"} {
		if !contains(m.Frameworks, f) {
			t.Errorf("framework %s missing in %v", f, m.Frameworks)
		}
	}
	for _, i := range []string{"stripe", "paystack", "sentry", "mixpanel", "redis"} {
		if !contains(m.Integrations, i) {
			t.Errorf("integration %s missing in %v", i, m.Integrations)
		}
	}
	for _, n := range []string{"STRIPE_SECRET_KEY", "DATABASE_URL", "REDIS_URL", "MIXPANEL_TOKEN", "SENTRY_DSN"} {
		if !contains(m.EnvNames, n) {
			t.Errorf("env %s missing", n)
		}
	}
	feat := map[string]string{}
	for _, f := range m.Features {
		feat[f.Name] = f.Source
	}
	if feat["orders"] != "route" || feat["new-checkout"] != "flag" || feat["dark_mode"] != "config" || feat["billing"] != "dir" {
		t.Errorf("features %v", feat)
	}
	if !m.Infra.Docker || !m.Infra.Compose || !contains(m.Infra.CI, "github-actions") || !contains(m.Infra.HostingHints, "vercel") {
		t.Errorf("infra %+v", m.Infra)
	}
	if m.Tests.Files != 1 || !contains(m.Tests.Frameworks, "go test") || !contains(m.Tests.Frameworks, "jest") {
		t.Errorf("tests %+v", m.Tests)
	}
	if !m.Security.Lockfile || !m.Security.EnvCommitted || m.Security.SecretsSuspected < 2 {
		t.Errorf("security %+v", m.Security)
	}
	if len(m.Description) > 2048 || !strings.HasPrefix(m.Description, "Shop A store for things") {
		t.Errorf("description %q", m.Description[:40])
	}
	prices := map[string]float64{}
	for _, p := range m.PricingHints {
		prices[p.Plan] = p.Amount
	}
	if prices["starter"] != 29 || prices["pro"] != 99 {
		t.Errorf("pricing %v", m.PricingHints)
	}
	// safety: no env values, no secret strings, no source code in the serialised manifest
	raw, _ := json.Marshal(m)
	for _, bad := range []string{"supersecretvalue", "postgres://", "AKIA" + "ABCDEFGHIJKLMNOP", "sk_live_abc", "sk_test_abc", "package main", "mux.HandleFunc"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("manifest leaks %q", bad)
		}
	}
}

func TestScanFileCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20050; i++ {
		write(t, root, filepath.Join("d", "f"+itoa(i)+".js"), "x\n")
	}
	m, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Languages["js"] != 20000 {
		t.Errorf("scanned %d files, cap is 20000", m.Languages["js"])
	}
}

func TestScanMissingDir(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error")
	}
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }
