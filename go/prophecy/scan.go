package prophecy

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	scanMaxFiles = 20000
	scanMaxBytes = 1 << 20
	maxDeps      = 2000
	maxRoutes    = 3000
	maxFeatures  = 150
	maxEnvNames  = 500
)

// Manifest is the codebase summary sent to Prophecy. It never contains source code, env values or secrets.
type Manifest struct {
	Schema       int               `json:"schema"`
	ScannedAt    string            `json:"scanned_at"`
	RootName     string            `json:"root_name"`
	Languages    map[string]int    `json:"languages"`
	LOC          int               `json:"loc"`
	Frameworks   []string          `json:"frameworks"`
	Dependencies []ManifestDep     `json:"dependencies"`
	Routes       []ManifestRoute   `json:"routes"`
	Features     []ManifestFeature `json:"features"`
	Integrations []string          `json:"integrations"`
	Infra        ManifestInfra     `json:"infra"`
	EnvNames     []string          `json:"env_names"`
	PricingHints []PricingHint     `json:"pricing_hints"`
	Description  string            `json:"description"`
	Tests        ManifestTests     `json:"tests"`
	Security     ManifestSecurity  `json:"security"`
}

type ManifestDep struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}
type ManifestRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	File   string `json:"file"`
}
type ManifestFeature struct {
	Name   string   `json:"name"`
	Source string   `json:"source"`
	Paths  []string `json:"paths"`
}
type ManifestInfra struct {
	Docker       bool     `json:"docker"`
	Compose      bool     `json:"compose"`
	Kubernetes   bool     `json:"kubernetes"`
	Terraform    bool     `json:"terraform"`
	Serverless   bool     `json:"serverless"`
	CI           []string `json:"ci"`
	HostingHints []string `json:"hosting_hints"`
}
type PricingHint struct {
	Plan     string  `json:"plan"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}
type ManifestTests struct {
	Files      int      `json:"files"`
	Frameworks []string `json:"frameworks"`
}
type ManifestSecurity struct {
	Lockfile         bool `json:"lockfile"`
	EnvCommitted     bool `json:"env_committed"`
	SecretsSuspected int  `json:"secrets_suspected"`
}

var skipDirs = map[string]bool{"node_modules": true, ".git": true, "vendor": true, "venv": true, ".venv": true,
	"dist": true, "build": true, "__pycache__": true, ".next": true, ".nuxt": true, ".cache": true, "coverage": true,
	".tox": true, "target": true, ".gradle": true, ".idea": true, ".terraform": true, "bower_components": true}

var codeExts = map[string]bool{"go": true, "py": true, "js": true, "mjs": true, "cjs": true, "ts": true, "tsx": true,
	"jsx": true, "php": true, "rb": true, "java": true, "kt": true, "kts": true, "swift": true, "dart": true, "rs": true,
	"c": true, "h": true, "cc": true, "cpp": true, "cs": true, "vue": true, "svelte": true, "scala": true, "ex": true,
	"exs": true, "lua": true, "sh": true, "sql": true, "html": true, "css": true, "scss": true}

var textExts = map[string]bool{"json": true, "yml": true, "yaml": true, "toml": true, "ini": true, "cfg": true,
	"conf": true, "env": true, "txt": true, "md": true, "mod": true, "lock": true, "xml": true, "tf": true, "properties": true}

var integrationKeys = [][2]string{{"stripe", "stripe"}, {"paddle", "paddle"}, {"paystack", "paystack"},
	{"flutterwave", "flutterwave"}, {"bachs", "bachs"}, {"auth0", "auth0"}, {"clerk", "clerk"}, {"supabase", "supabase"},
	{"firebase", "firebase"}, {"sentry", "sentry"}, {"segment", "segment"}, {"mixpanel", "mixpanel"},
	{"posthog", "posthog"}, {"twilio", "twilio"}, {"sendgrid", "sendgrid"}, {"resend", "resend"}, {"openai", "openai"},
	{"anthropic", "anthropic"}, {"aws-sdk", "s3"}, {"boto3", "s3"}, {"aws_s3", "s3"}, {"s3_bucket", "s3"},
	{"mailgun", "mailgun"}, {"postmark", "postmark"}, {"algolia", "algolia"}, {"cloudinary", "cloudinary"},
	{"plaid", "plaid"}, {"shopify", "shopify"}, {"datadog", "datadog"}, {"newrelic", "newrelic"},
	{"linearjeph", "linearjeph"}, {"redis", "redis"}, {"kafka", "kafka"}, {"rabbitmq", "rabbitmq"}}

var frameworkDeps = map[string]string{
	"npm:express": "express", "npm:next": "next", "npm:react": "react", "npm:vue": "vue", "npm:nuxt": "nuxt",
	"npm:svelte": "svelte", "npm:@nestjs/core": "nestjs", "npm:fastify": "fastify", "npm:koa": "koa", "npm:hono": "hono",
	"npm:@angular/core": "angular", "pypi:django": "django", "pypi:flask": "flask", "pypi:fastapi": "fastapi",
	"pypi:tornado": "tornado", "pypi:starlette": "starlette", "composer:laravel/framework": "laravel",
	"composer:symfony/framework-bundle": "symfony", "composer:slim/slim": "slim", "gem:rails": "rails",
	"gem:sinatra": "sinatra", "gem:hanami": "hanami", "gem:roda": "roda",
}

var goFrameworkMods = map[string]string{"github.com/gin-gonic/gin": "gin", "github.com/go-chi/chi": "chi",
	"github.com/labstack/echo": "echo", "github.com/gofiber/fiber": "fiber", "github.com/gorilla/mux": "gorilla-mux"}

var testDeps = map[string]string{"jest": "jest", "vitest": "vitest", "mocha": "mocha", "@playwright/test": "playwright",
	"cypress": "cypress", "pytest": "pytest", "phpunit/phpunit": "phpunit", "pestphp/pest": "pest", "rspec": "rspec",
	"rspec-rails": "rspec", "minitest": "minitest"}

var (
	reGoRoute   = regexp.MustCompile("\\b(\\w+)\\.(Get|Post|Put|Patch|Delete|Head|Options|GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any|Handle|HandleFunc)\\(\\s*(?:\"([^\"]+)\"|`([^`]+)`)")
	reGoGroup   = regexp.MustCompile(`(\w+)\s*:?=\s*(\w+)\.(?:Group|Route)\(\s*"([^"]*)"`)
	reJSRoute   = regexp.MustCompile("\\b(?:app|router|server|fastify|api|routes)\\.(get|post|put|patch|delete|all)\\(\\s*['\"`]([^'\"`]+)['\"`]")
	rePHPRoute  = regexp.MustCompile(`Route::(get|post|put|patch|delete|any|resource|apiResource)\(\s*['"]([^'"]+)['"]`)
	reRBRoute   = regexp.MustCompile(`^\s*(get|post|put|patch|delete)\s+['"]([^'"]+)['"]`)
	reRBRes     = regexp.MustCompile(`^\s*resources?\s+:(\w+)`)
	reRBNS      = regexp.MustCompile(`^\s*(?:namespace|scope)\s+:?['"]?(\w+)['"]?.*\bdo\b`)
	reRBDo      = regexp.MustCompile(`\bdo\s*(\|[^|]*\|)?\s*$`)
	reRBEnd     = regexp.MustCompile(`^\s*end\b`)
	rePyDjango  = regexp.MustCompile(`\b(?:re_)?path\(\s*r?['"]([^'"]*)['"]`)
	rePyDeco    = regexp.MustCompile(`@\w+\.(route|get|post|put|patch|delete)\(\s*['"]([^'"]+)['"]`)
	reFlag      = regexp.MustCompile(`(?i)(?:isEnabled|is_enabled|isFeatureEnabled|feature_enabled\??|featureEnabled|flagEnabled|Feature::active|useFlag|useFeatureFlag|getBooleanFlag|feature_flag|FeatureFlag)\(\s*['"]([\w.:-]{2,60})['"]`)
	reEnvFile   = regexp.MustCompile(`^\s*(?:export\s+)?([A-Z][A-Z0-9_]*)\s*=`)
	reEnvCode1  = regexp.MustCompile(`(?:Getenv|LookupEnv|getenv|env|fetch|get|config)\(\s*["']([A-Z][A-Z0-9_]{1,63})["']`)
	reEnvCode2  = regexp.MustCompile(`(?:process\.env|import\.meta\.env)\.([A-Z][A-Z0-9_]{1,63})`)
	reEnvCode3  = regexp.MustCompile(`(?:ENV|environ|_ENV|_SERVER)\[\s*["']([A-Z][A-Z0-9_]{1,63})["']`)
	rePlanName  = regexp.MustCompile(`(?i)\b(?:name|plan|id|slug|key|tier)\b["']?\s*(?:=>|[:=])\s*["']([A-Za-z][\w .-]{0,39})["']`)
	rePrice     = regexp.MustCompile(`(?i)\b(?:price|amount|monthly|mrr|cost)\w*["']?\s*(?:=>|[:=])\s*(\d{1,6}(?:\.\d{1,2})?)\b`)
	reCurrency  = regexp.MustCompile(`(?i)\b(USD|EUR|GBP|NGN|GHS|KES|ZAR|CAD|AUD|INR)\b`)
	rePriceFile = regexp.MustCompile(`(?i)(plan|pricing|price|billing|subscription|tier)`)
	reReq       = regexp.MustCompile(`^([A-Za-z0-9_.\-]+)(?:\[[^\]]*\])?\s*(?:(==|>=|<=|~=|!=|>|<)\s*([\w.*+-]+))?`)
	reTagStrip  = regexp.MustCompile(`<[^>]+>`)
	reImgStrip  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLinkStrip = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reSecrets   = []*regexp.Regexp{
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
		regexp.MustCompile(`sk_live_[0-9A-Za-z]{16,}`),
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
		regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`ljp_sk_[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
	}
)

type scanState struct {
	m          Manifest
	deps       map[string]bool
	depList    []ManifestDep
	routeSeen  map[string]bool
	envSet     map[string]bool
	dirFiles   map[string]int
	flagSeen   map[string]bool
	flags      []ManifestFeature
	envFeat    []ManifestFeature
	ci         map[string]bool
	hosting    map[string]bool
	testFw     map[string]bool
	priceSeen  map[string]bool
	hasGoTests bool
	gemLock    bool
	gemfiles   []string
	files      int
}

func (s *scanState) addDep(eco, name, ver string) {
	k := eco + ":" + name
	if s.deps[k] || len(s.depList) >= maxDeps || name == "" {
		return
	}
	s.deps[k] = true
	s.depList = append(s.depList, ManifestDep{eco, name, ver})
}

func (s *scanState) addRoute(method, p, file string) {
	if len(s.m.Routes) >= maxRoutes || p == "" {
		return
	}
	method = strings.ToUpper(method)
	p = rePathParam.ReplaceAllString(p, ":$1")
	k := method + " " + p + " " + file
	if s.routeSeen[k] {
		return
	}
	s.routeSeen[k] = true
	s.m.Routes = append(s.m.Routes, ManifestRoute{method, p, file})
}

func (s *scanState) addEnv(n string) {
	if len(s.envSet) < maxEnvNames {
		s.envSet[n] = true
	}
}

func isBinary(b []byte) bool {
	if len(b) > 512 {
		b = b[:512]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// Scan walks root and builds a Manifest. It reads files locally and never includes source code, env values or secrets.
func Scan(root string) (Manifest, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Manifest{}, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		if err == nil {
			err = fs.ErrInvalid
		}
		return Manifest{}, err
	}
	s := &scanState{deps: map[string]bool{}, routeSeen: map[string]bool{}, envSet: map[string]bool{},
		dirFiles: map[string]int{}, flagSeen: map[string]bool{}, ci: map[string]bool{}, hosting: map[string]bool{},
		testFw: map[string]bool{}, priceSeen: map[string]bool{}}
	s.m = Manifest{Schema: 1, ScannedAt: time.Now().UTC().Format(time.RFC3339), RootName: filepath.Base(abs),
		Languages: map[string]int{}, Frameworks: []string{}, Dependencies: []ManifestDep{}, Routes: []ManifestRoute{},
		Features: []ManifestFeature{}, Integrations: []string{}, EnvNames: []string{}, PricingHints: []PricingHint{}}
	s.m.Infra.CI = []string{}
	s.m.Infra.HostingHints = []string{}
	s.m.Tests.Frameworks = []string{}

	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if p == abs {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never follow symlinks: no escape from root
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			s.dirInfra(rel, d.Name())
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if s.files >= scanMaxFiles {
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > scanMaxBytes {
			return nil
		}
		s.files++
		s.file(abs, p, rel, d.Name())
		return nil
	})
	return s.finish(abs), nil
}

func (s *scanState) dirInfra(rel, name string) {
	switch name {
	case "k8s", "kubernetes", "helm", "charts":
		s.m.Infra.Kubernetes = true
	}
	if rel == ".github/workflows" {
		s.ci["github-actions"] = true
	}
	if rel == ".circleci" {
		s.ci["circleci"] = true
	}
}

func (s *scanState) fileInfra(rel, base string) {
	lb := strings.ToLower(base)
	switch {
	case lb == "dockerfile" || strings.HasPrefix(lb, "dockerfile.") || strings.HasSuffix(lb, ".dockerfile"):
		s.m.Infra.Docker = true
	case lb == "docker-compose.yml" || lb == "docker-compose.yaml" || lb == "compose.yml" || lb == "compose.yaml" ||
		strings.HasPrefix(lb, "docker-compose."):
		s.m.Infra.Compose = true
	case strings.HasSuffix(lb, ".tf"):
		s.m.Infra.Terraform = true
	case lb == "serverless.yml" || lb == "serverless.yaml" || lb == "serverless.ts" || lb == "serverless.js":
		s.m.Infra.Serverless = true
	case lb == ".gitlab-ci.yml":
		s.ci["gitlab"] = true
	case lb == "jenkinsfile":
		s.ci["jenkins"] = true
	case lb == "azure-pipelines.yml":
		s.ci["azure-pipelines"] = true
	case lb == "bitbucket-pipelines.yml":
		s.ci["bitbucket"] = true
	case lb == ".travis.yml":
		s.ci["travis"] = true
	case lb == "vercel.json":
		s.hosting["vercel"] = true
	case lb == "netlify.toml":
		s.hosting["netlify"] = true
	case lb == "fly.toml":
		s.hosting["fly"] = true
	case lb == "render.yaml" || lb == "render.yml":
		s.hosting["render"] = true
	case lb == "procfile":
		s.hosting["heroku"] = true
	case lb == "railway.json" || lb == "railway.toml":
		s.hosting["railway"] = true
	case lb == "wrangler.toml" || lb == "wrangler.json":
		s.hosting["cloudflare"] = true
	case lb == "firebase.json":
		s.hosting["firebase"] = true
	case lb == "app.yaml" && !strings.Contains(rel, "/"):
		s.hosting["gcp-app-engine"] = true
	}
	if strings.Contains(rel, "k8s/") || strings.Contains(rel, "kubernetes/") {
		s.m.Infra.Kubernetes = true
	}
}

func isTestFile(rel, base string) bool {
	lb := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lb, "_test.go"), strings.HasPrefix(lb, "test_") && strings.HasSuffix(lb, ".py"),
		strings.HasSuffix(lb, "_test.py"), strings.HasSuffix(lb, "_spec.rb"), strings.HasSuffix(lb, "_test.rb"),
		strings.HasSuffix(lb, "test.php"), strings.HasSuffix(lb, "_test.exs"):
		return true
	}
	for _, mid := range []string{".test.", ".spec."} {
		if strings.Contains(lb, mid) {
			return true
		}
	}
	return false
}

func (s *scanState) file(abs, full, rel, base string) {
	s.fileInfra(rel, base)
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(base), "."))
	lb := strings.ToLower(base)
	isEnvFile := strings.HasPrefix(lb, ".env") || strings.HasSuffix(lb, ".env")
	if isEnvFile {
		if !strings.Contains(lb, "example") && !strings.Contains(lb, "sample") && !strings.Contains(lb, "template") &&
			!strings.Contains(lb, "dist") {
			s.m.Security.EnvCommitted = true
		}
	}
	switch lb {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "composer.lock", "go.sum", "poetry.lock", "pipfile.lock", "uv.lock", "bun.lockb":
		s.m.Security.Lockfile = true
	case "gemfile.lock":
		s.m.Security.Lockfile = true
		s.gemLock = true
	case "artisan":
		s.m.Frameworks = append(s.m.Frameworks, "laravel")
	case "manage.py":
		s.m.Frameworks = append(s.m.Frameworks, "django")
	case "conftest.py":
		s.testFw["pytest"] = true
	case "phpunit.xml", "phpunit.xml.dist":
		s.testFw["phpunit"] = true
	}
	if rel == "config/routes.rb" {
		s.m.Frameworks = append(s.m.Frameworks, "rails")
	}
	code := codeExts[ext]
	if !code && !textExts[ext] && !isEnvFile && lb != "gemfile" && lb != "gemfile.lock" && !strings.HasPrefix(lb, "readme") &&
		lb != "requirements.txt" {
		return
	}
	data, err := os.ReadFile(full)
	if err != nil || isBinary(data) {
		return
	}
	text := string(data)
	if code {
		s.m.Languages[ext]++
		for _, ln := range strings.Split(text, "\n") {
			if strings.TrimSpace(ln) != "" {
				s.m.LOC++
			}
		}
		if isTestFile(rel, base) {
			s.m.Tests.Files++
			switch {
			case strings.HasSuffix(lb, "_test.go"):
				s.testFw["go test"] = true
			case ext == "py":
				s.testFw["pytest"] = s.testFw["pytest"] || strings.Contains(text, "pytest")
				if strings.Contains(text, "unittest") {
					s.testFw["unittest"] = true
				}
			case ext == "rb" && strings.HasSuffix(lb, "_spec.rb"):
				s.testFw["rspec"] = true
			case ext == "rb":
				s.testFw["minitest"] = true
			case ext == "php":
				s.testFw["phpunit"] = true
			}
		}
		if i := strings.Index(rel, "/"); i > 0 {
			top := rel[:i]
			switch top {
			case "src", "app", "apps", "packages", "internal", "pkg", "modules", "lib", "cmd", "services":
				parts := strings.SplitN(rel, "/", 3)
				if len(parts) == 3 {
					s.dirFiles[top+"/"+parts[1]]++
				}
			default:
				s.dirFiles[top]++
			}
		}
		s.routes(rel, ext, text)
		s.envFromCode(text)
		for _, m := range reFlag.FindAllStringSubmatch(text, -1) {
			n := strings.ToLower(m[1])
			if !s.flagSeen[n] && len(s.flags) < 100 {
				s.flagSeen[n] = true
				s.flags = append(s.flags, ManifestFeature{n, "flag", []string{rel}})
			}
		}
	}
	if isEnvFile {
		for _, ln := range strings.Split(text, "\n") {
			if m := reEnvFile.FindStringSubmatch(ln); m != nil {
				s.addEnv(m[1])
			}
		}
	}
	s.manifestFile(rel, lb, text)
	if strings.HasPrefix(lb, "readme") && !strings.Contains(rel, "/") && s.m.Description == "" {
		s.m.Description = describe(data)
	}
	for _, re := range reSecrets {
		s.m.Security.SecretsSuspected += len(re.FindAllStringIndex(text, -1))
	}
	if rePriceFile.MatchString(rel) && len(s.m.PricingHints) < 20 {
		switch ext {
		case "json", "js", "ts", "py", "php", "rb", "go", "yml", "yaml":
			s.pricing(text)
		}
	}
}

func (s *scanState) envFromCode(text string) {
	for _, re := range []*regexp.Regexp{reEnvCode1, reEnvCode2, reEnvCode3} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			s.addEnv(m[1])
		}
	}
}

func (s *scanState) pricing(text string) {
	plan, cur := "", ""
	for _, ln := range strings.Split(text, "\n") {
		if len(ln) > 2000 {
			continue
		}
		if m := reCurrency.FindStringSubmatch(ln); m != nil {
			cur = strings.ToUpper(m[1])
		}
		// walk plan names and prices in order of appearance on the line
		plans := rePlanName.FindAllStringSubmatchIndex(ln, -1)
		prices := rePrice.FindAllStringSubmatchIndex(ln, -1)
		pi := 0
		for _, pr := range prices {
			for pi < len(plans) && plans[pi][0] < pr[0] {
				plan = strings.TrimSpace(ln[plans[pi][2]:plans[pi][3]])
				pi++
			}
			num := ln[pr[2]:pr[3]]
			var amt float64
			if err := json.Unmarshal([]byte(num), &amt); err != nil || amt <= 0 {
				continue
			}
			name := plan
			if name == "" {
				name = "default"
			}
			c := cur
			if c == "" {
				c = "USD"
			}
			k := name + "|" + num
			if s.priceSeen[k] || len(s.m.PricingHints) >= 20 {
				continue
			}
			s.priceSeen[k] = true
			s.m.PricingHints = append(s.m.PricingHints, PricingHint{name, amt, c})
		}
		for ; pi < len(plans); pi++ {
			plan = strings.TrimSpace(ln[plans[pi][2]:plans[pi][3]])
		}
	}
}

func describe(data []byte) string {
	if len(data) > 2048 {
		data = data[:2048]
	}
	t := strings.ToValidUTF8(string(data), "")
	t = reImgStrip.ReplaceAllString(t, "")
	t = reLinkStrip.ReplaceAllString(t, "$1")
	t = reTagStrip.ReplaceAllString(t, "")
	t = strings.NewReplacer("```", " ", "`", "", "#", " ", "*", "", "_ _", " ").Replace(t)
	return strings.Join(strings.Fields(t), " ")
}

func (s *scanState) routes(rel, ext, text string) {
	switch ext {
	case "go":
		prefix := map[string]string{}
		for _, m := range reGoGroup.FindAllStringSubmatch(text, -1) {
			prefix[m[1]] = prefix[m[2]] + m[3]
		}
		for _, m := range reGoRoute.FindAllStringSubmatch(text, -1) {
			p := m[3]
			if p == "" {
				p = m[4]
			}
			method := strings.ToUpper(m[2])
			if method == "HANDLE" || method == "HANDLEFUNC" {
				method = "ANY"
				if f := strings.Fields(p); len(f) == 2 {
					method, p = f[0], f[1]
				}
				if i := strings.Index(p, "/"); i > 0 {
					p = p[i:]
				}
			}
			if !strings.HasPrefix(p, "/") {
				continue
			}
			s.addRoute(method, prefix[m[1]]+p, rel)
		}
	case "php":
		for _, m := range rePHPRoute.FindAllStringSubmatch(text, -1) {
			p := "/" + strings.TrimPrefix(m[2], "/")
			switch m[1] {
			case "resource", "apiResource":
				s.addRoute("GET", p, rel)
				s.addRoute("POST", p, rel)
				s.addRoute("GET", p+"/:id", rel)
				s.addRoute("PUT", p+"/:id", rel)
				s.addRoute("DELETE", p+"/:id", rel)
			default:
				s.addRoute(strings.ToUpper(m[1]), laravelParams(p), rel)
			}
		}
	case "rb":
		if !strings.HasSuffix(rel, "routes.rb") && !strings.Contains(rel, "routes/") {
			if strings.Contains(text, "Sinatra") || strings.Contains(text, "Roda") {
				for _, ln := range strings.Split(text, "\n") {
					if m := reRBRoute.FindStringSubmatch(ln); m != nil {
						s.addRoute(m[1], rubyParams(m[2]), rel)
					}
				}
			}
			return
		}
		var stack []string // "" for non-namespace blocks
		for _, ln := range strings.Split(text, "\n") {
			ln = strings.TrimRight(ln, "\r")
			if reRBEnd.MatchString(ln) {
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				continue
			}
			pre := strings.Join(nonEmpty(stack), "/")
			if pre != "" {
				pre = "/" + pre
			}
			if m := reRBNS.FindStringSubmatch(ln); m != nil {
				stack = append(stack, m[1])
				continue
			}
			if m := reRBRes.FindStringSubmatch(ln); m != nil {
				p := pre + "/" + m[1]
				if strings.HasPrefix(strings.TrimSpace(ln), "resources") {
					for _, r := range [][2]string{{"GET", p}, {"POST", p}, {"GET", p + "/:id"}, {"PUT", p + "/:id"}, {"DELETE", p + "/:id"}} {
						s.addRoute(r[0], r[1], rel)
					}
				} else {
					s.addRoute("GET", p, rel)
				}
			} else if m := reRBRoute.FindStringSubmatch(ln); m != nil {
				s.addRoute(m[1], pre+"/"+strings.TrimPrefix(rubyParams(m[2]), "/"), rel)
			}
			if reRBDo.MatchString(ln) {
				stack = append(stack, "")
			}
		}
	case "py":
		if strings.HasSuffix(rel, "urls.py") {
			for _, m := range rePyDjango.FindAllStringSubmatch(text, -1) {
				s.addRoute("ANY", "/"+strings.TrimPrefix(djangoParams(m[1]), "/"), rel)
			}
		}
		for _, m := range rePyDeco.FindAllStringSubmatch(text, -1) {
			method := strings.ToUpper(m[1])
			if method == "ROUTE" {
				method = "ANY"
			}
			if strings.HasPrefix(m[2], "/") {
				s.addRoute(method, m[2], rel)
			}
		}
	case "js", "mjs", "cjs", "ts", "jsx", "tsx":
		for _, m := range reJSRoute.FindAllStringSubmatch(text, -1) {
			if strings.HasPrefix(m[2], "/") || m[2] == "*" {
				s.addRoute(strings.ToUpper(strings.Replace(m[1], "all", "any", 1)), m[2], rel)
			}
		}
		s.nextRoute(rel, ext)
	}
}

func nonEmpty(a []string) []string {
	var out []string
	for _, x := range a {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

var (
	rePathParam    = regexp.MustCompile(`\{(\w+)[^}]*\}`)
	reLaravelParam = regexp.MustCompile(`\{(\w+)\??\}`)
	reRubyParam    = regexp.MustCompile(`:(\w+)`)
	reDjangoParam  = regexp.MustCompile(`<(?:\w+:)?(\w+)>`)
)

func laravelParams(p string) string { return reLaravelParam.ReplaceAllString(p, ":$1") }
func rubyParams(p string) string    { return p }
func djangoParams(p string) string  { return reDjangoParam.ReplaceAllString(p, ":$1") }

func (s *scanState) nextRoute(rel, ext string) {
	r := rel
	r = strings.TrimPrefix(r, "src/")
	var kind, sub string
	switch {
	case strings.HasPrefix(r, "app/"):
		kind, sub = "app", r[4:]
	case strings.HasPrefix(r, "pages/"):
		kind, sub = "pages", r[6:]
	default:
		return
	}
	noext := strings.TrimSuffix(sub, "."+ext)
	segs := strings.Split(noext, "/")
	last := segs[len(segs)-1]
	segs = segs[:len(segs)-1]
	var out []string
	conv := func(x string) string {
		switch {
		case strings.HasPrefix(x, "(") && strings.HasSuffix(x, ")"):
			return ""
		case strings.HasPrefix(x, "[") && strings.HasSuffix(x, "]"):
			return ":" + strings.Trim(x, "[].")
		}
		return x
	}
	for _, x := range segs {
		if c := conv(x); c != "" {
			out = append(out, c)
		}
	}
	method := "GET"
	if kind == "app" {
		switch last {
		case "page":
		case "route":
			method = "ANY"
		default:
			return
		}
	} else {
		if strings.HasPrefix(last, "_") {
			return
		}
		if last != "index" {
			if c := conv(last); c != "" {
				out = append(out, c)
			}
		}
		if len(out) > 0 && out[0] == "api" {
			method = "ANY"
		}
	}
	s.addRoute(method, "/"+strings.Join(out, "/"), rel)
}

func (s *scanState) manifestFile(rel, lb, text string) {
	switch {
	case lb == "go.mod":
		inBlock := false
		for _, ln := range strings.Split(text, "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "require (") {
				inBlock = true
				continue
			}
			if inBlock && ln == ")" {
				inBlock = false
				continue
			}
			if !inBlock {
				if !strings.HasPrefix(ln, "require ") {
					continue
				}
				ln = strings.TrimSpace(strings.TrimPrefix(ln, "require "))
			}
			if strings.Contains(ln, "// indirect") {
				continue
			}
			f := strings.Fields(ln)
			if len(f) >= 2 && !strings.HasPrefix(f[0], "//") {
				s.addDep("go", f[0], f[1])
			}
		}
	case lb == "package.json":
		var pj map[string]json.RawMessage
		if json.Unmarshal([]byte(text), &pj) != nil {
			return
		}
		for _, k := range []string{"dependencies", "devDependencies"} {
			var m map[string]string
			if json.Unmarshal(pj[k], &m) == nil {
				for _, n := range sortedKeys(m) {
					s.addDep("npm", n, m[n])
				}
			}
		}
	case lb == "composer.json":
		var pj map[string]json.RawMessage
		if json.Unmarshal([]byte(text), &pj) != nil {
			return
		}
		for _, k := range []string{"require", "require-dev"} {
			var m map[string]string
			if json.Unmarshal(pj[k], &m) == nil {
				for _, n := range sortedKeys(m) {
					if n == "php" || strings.HasPrefix(n, "ext-") || strings.HasPrefix(n, "lib-") {
						continue
					}
					s.addDep("composer", n, m[n])
				}
			}
		}
	case lb == "gemfile.lock":
		inGem := false
		for _, ln := range strings.Split(text, "\n") {
			ln = strings.TrimRight(ln, "\r")
			if !strings.HasPrefix(ln, " ") {
				inGem = strings.TrimSpace(ln) == "GEM" || strings.HasPrefix(ln, "GEM")
				continue
			}
			if inGem && strings.HasPrefix(ln, "    ") && !strings.HasPrefix(ln, "     ") {
				t := strings.TrimSpace(ln)
				if i := strings.Index(t, " ("); i > 0 && strings.HasSuffix(t, ")") {
					s.addDep("gem", t[:i], t[i+2:len(t)-1])
				}
			}
		}
	case lb == "gemfile":
		re := regexp.MustCompile(`^\s*gem\s+['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?`)
		for _, ln := range strings.Split(text, "\n") {
			if m := re.FindStringSubmatch(ln); m != nil {
				s.addDep("gem", m[1], m[2])
			}
		}
	case strings.HasPrefix(lb, "requirements") && strings.HasSuffix(lb, ".txt"):
		for _, ln := range strings.Split(text, "\n") {
			s.pyReq(ln)
		}
	case lb == "pyproject.toml":
		section, inArr := "", false
		for _, ln := range strings.Split(text, "\n") {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "[") {
				section, inArr = strings.Trim(t, "[] "), false
				continue
			}
			if section == "project" && strings.HasPrefix(t, "dependencies") && strings.Contains(t, "[") {
				inArr = true
				t = t[strings.Index(t, "[")+1:]
			}
			if inArr {
				for _, q := range regexp.MustCompile(`"([^"]+)"|'([^']+)'`).FindAllStringSubmatch(t, -1) {
					v := q[1]
					if v == "" {
						v = q[2]
					}
					s.pyReq(v)
				}
				if strings.Contains(t, "]") {
					inArr = false
				}
				continue
			}
			if section == "tool.poetry.dependencies" || section == "tool.poetry.dev-dependencies" ||
				section == "tool.poetry.group.dev.dependencies" {
				if i := strings.Index(t, "="); i > 0 && !strings.HasPrefix(t, "#") {
					name := strings.TrimSpace(t[:i])
					if name == "python" {
						continue
					}
					ver := strings.Trim(strings.TrimSpace(t[i+1:]), `"'`)
					if m := regexp.MustCompile(`version\s*=\s*["']([^"']+)`).FindStringSubmatch(t); m != nil {
						ver = m[1]
					}
					s.addDep("pypi", strings.ToLower(name), ver)
				}
			}
		}
	}
}

func (s *scanState) pyReq(ln string) {
	ln = strings.TrimSpace(ln)
	if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, "-") {
		return
	}
	if i := strings.Index(ln, ";"); i > 0 {
		ln = ln[:i]
	}
	m := reReq.FindStringSubmatch(ln)
	if m == nil {
		return
	}
	ver := m[3]
	if m[2] != "" && m[2] != "==" {
		ver = m[2] + m[3]
	}
	s.addDep("pypi", strings.ToLower(m[1]), ver)
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

func (s *scanState) finish(abs string) Manifest {
	m := s.m
	m.Dependencies = s.depList
	if m.Dependencies == nil {
		m.Dependencies = []ManifestDep{}
	}
	fw := map[string]bool{}
	for _, f := range m.Frameworks {
		fw[f] = true
	}
	lowerDeps := []string{}
	for _, d := range s.depList {
		if n, ok := frameworkDeps[d.Ecosystem+":"+d.Name]; ok {
			fw[n] = true
		}
		if d.Ecosystem == "go" {
			for prefix, n := range goFrameworkMods {
				if strings.HasPrefix(d.Name, prefix) {
					fw[n] = true
				}
			}
		}
		if n, ok := testDeps[d.Name]; ok {
			s.testFw[n] = true
		}
		lowerDeps = append(lowerDeps, strings.ToLower(d.Name))
	}
	for _, r := range m.Routes {
		if strings.HasSuffix(r.File, ".go") && fw["gin"] == false && fw["chi"] == false && fw["echo"] == false && fw["fiber"] == false {
			fw["net/http"] = true
		}
	}
	m.Frameworks = setList(fw)
	// integrations: dependency names and env names only
	integ := map[string]bool{}
	envNames := setList(s.envSet)
	hay := append([]string{}, lowerDeps...)
	for _, e := range envNames {
		hay = append(hay, strings.ToLower(e))
	}
	for _, kv := range integrationKeys {
		for _, h := range hay {
			if strings.Contains(h, kv[0]) {
				integ[kv[1]] = true
				break
			}
		}
	}
	m.Integrations = setList(integ)
	m.EnvNames = envNames
	// features: routes, flags, config, dirs
	groups := map[string][]string{}
	var order []string
	for _, r := range m.Routes {
		n := routeGroup(r.Path)
		if n == "" {
			continue
		}
		if _, ok := groups[n]; !ok {
			order = append(order, n)
		}
		if len(groups[n]) < 5 {
			groups[n] = append(groups[n], r.Path)
		}
	}
	seen := map[string]bool{}
	for _, n := range order {
		seen[n] = true
		m.Features = append(m.Features, ManifestFeature{n, "route", groups[n]})
	}
	for _, f := range s.flags {
		if !seen[f.Name] {
			seen[f.Name] = true
			m.Features = append(m.Features, f)
		}
	}
	for _, e := range envNames {
		for _, pfx := range []string{"FEATURE_", "FF_", "ENABLE_", "ENABLED_"} {
			if strings.HasPrefix(e, pfx) {
				n := strings.ToLower(strings.TrimPrefix(e, pfx))
				if n != "" && !seen[n] {
					seen[n] = true
					m.Features = append(m.Features, ManifestFeature{n, "config", []string{e}})
				}
			}
		}
	}
	var dirs []string
	for d := range s.dirFiles {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		n := strings.ToLower(path.Base(d))
		if seen[n] || strings.HasPrefix(n, ".") {
			continue
		}
		switch n {
		case "src", "lib", "test", "tests", "spec", "docs", "scripts", "public", "static", "assets", "utils", "common", "types",
			"config", "migrations", "database", "bin", "tmp", "examples", "e2e", "__tests__", "node_modules", "app":
			continue
		}
		seen[n] = true
		m.Features = append(m.Features, ManifestFeature{n, "dir", []string{d}})
	}
	if len(m.Features) > maxFeatures {
		m.Features = m.Features[:maxFeatures]
	}
	// infra
	m.Infra.CI = setList(s.ci)
	m.Infra.HostingHints = setList(s.hosting)
	if s.m.Infra.Kubernetes {
		m.Infra.Kubernetes = true
	}
	m.Tests.Frameworks = setList(s.testFw)
	return m
}

func routeGroup(p string) string {
	for _, seg := range strings.Split(p, "/") {
		l := strings.ToLower(seg)
		if l == "" || l == "api" || l == "*" || strings.HasPrefix(l, ":") || strings.HasPrefix(l, "{") {
			continue
		}
		if len(l) >= 2 && l[0] == 'v' && l[1] >= '0' && l[1] <= '9' {
			continue
		}
		return l
	}
	return ""
}

func setList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
