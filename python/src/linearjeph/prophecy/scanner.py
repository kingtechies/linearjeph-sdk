"""Prophecy codebase scanner. Reads files locally and returns a manifest (schema 1): names, counts and route templates
only. Never source text, never env values, never matched secrets (secrets are only counted).

Limits: skips node_modules/.git/vendor/venv/dist/build..., files > 1 MB, symlinks, at most 20,000 files.
Standard library only; same output schema as the Node SDK scanner.
"""
import json
import os
import re
from datetime import datetime, timezone

MAX_FILES = 20000
MAX_BYTES = 1024 * 1024
CAPS = {"dependencies": 2000, "routes": 3000, "features": 200, "env_names": 500, "pricing_hints": 20}
SKIP_DIRS = {"node_modules", ".git", "vendor", "venv", ".venv", "env", "dist", "build", "__pycache__", ".next", ".nuxt",
             ".svelte-kit", "target", "coverage", ".tox", ".cache", "site-packages", ".idea", ".vscode", ".gradle", "Pods",
             "bower_components", ".terraform", ".mypy_cache", ".pytest_cache", ".turbo", "out"}
KEEP_DOT_DIRS = {".github", ".circleci"}
CODE_EXT = {"js", "jsx", "ts", "tsx", "mjs", "cjs", "py", "go", "php", "rb", "java", "kt", "rs", "cs", "swift", "dart", "vue",
            "svelte", "scala", "ex", "exs", "c", "cpp", "h", "sh", "sql", "html", "css", "scss"}
CONFIG_EXT = {"json", "yml", "yaml", "toml", "ini", "cfg", "xml", "tf", "properties", "gradle", "kts", "mod", "env", "md", "txt"}
LOCKFILES = {"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "poetry.lock", "Pipfile.lock", "uv.lock", "go.sum",
             "composer.lock", "Gemfile.lock"}
JS_EXT = ("js", "jsx", "ts", "tsx", "mjs", "cjs")
SECRET_RES = [re.compile(p) for p in (
    r"\bAKIA[0-9A-Z]{16}\b", r"\bsk_live_[0-9A-Za-z]{16,}", r"\bghp_[A-Za-z0-9]{30,}", r"\bgithub_pat_[A-Za-z0-9_]{40,}",
    r"-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----", r"\bxox[baprs]-[A-Za-z0-9-]{10,}",
    r"\bAIza[0-9A-Za-z_-]{35}", r"\bljp_sk_[A-Za-z0-9_-]{20,}")]
FRAMEWORK_DEPS = {
    "express": "express", "fastify": "fastify", "koa": "koa", "next": "next", "@nestjs/core": "nestjs", "react": "react", "vue": "vue",
    "nuxt": "nuxt", "svelte": "svelte", "@sveltejs/kit": "sveltekit", "@angular/core": "angular", "@hapi/hapi": "hapi",
    "@remix-run/node": "remix", "astro": "astro", "django": "django", "flask": "flask", "fastapi": "fastapi", "starlette": "starlette",
    "tornado": "tornado", "aiohttp": "aiohttp", "celery": "celery", "laravel/framework": "laravel",
    "symfony/framework-bundle": "symfony", "rails": "rails", "sinatra": "sinatra", "github.com/gin-gonic/gin": "gin",
    "github.com/go-chi/chi": "chi", "github.com/labstack/echo": "echo", "github.com/gofiber/fiber": "fiber",
    "org.springframework.boot": "spring-boot", "flutter": "flutter"}
INTEGRATIONS = {
    "stripe": (["stripe"], ["STRIPE"]), "paddle": (["paddle"], ["PADDLE"]), "paystack": (["paystack"], ["PAYSTACK"]),
    "flutterwave": (["flutterwave"], ["FLUTTERWAVE", "FLW_"]), "bachs": (["bachs"], ["BACHS"]), "auth0": (["auth0"], ["AUTH0"]),
    "clerk": (["@clerk", "clerk"], ["CLERK"]), "supabase": (["supabase"], ["SUPABASE"]), "firebase": (["firebase"], ["FIREBASE"]),
    "sentry": (["sentry"], ["SENTRY"]), "segment": (["@segment", "analytics-node", "segment"], ["SEGMENT"]),
    "mixpanel": (["mixpanel"], ["MIXPANEL"]), "posthog": (["posthog"], ["POSTHOG"]), "amplitude": (["amplitude"], ["AMPLITUDE"]),
    "launchdarkly": (["launchdarkly"], ["LAUNCHDARKLY", "LD_SDK"]), "unleash": (["unleash"], ["UNLEASH"]),
    "twilio": (["twilio"], ["TWILIO"]), "sendgrid": (["sendgrid"], ["SENDGRID"]), "resend": (["resend"], ["RESEND"]),
    "mailgun": (["mailgun"], ["MAILGUN"]), "postmark": (["postmark"], ["POSTMARK"]), "openai": (["openai"], ["OPENAI"]),
    "anthropic": (["anthropic"], ["ANTHROPIC"]),
    "s3": (["@aws-sdk/client-s3", "aws-sdk", "boto3", "aws/aws-sdk-php", "aws-sdk-s3"], ["AWS_S3", "S3_", "AWS_ACCESS"]),
    "cloudinary": (["cloudinary"], ["CLOUDINARY"]), "algolia": (["algolia"], ["ALGOLIA"]),
    "redis": (["redis", "ioredis"], ["REDIS"]), "rabbitmq": (["amqplib", "pika"], ["RABBITMQ", "AMQP"]),
    "kafka": (["kafkajs", "kafka-python", "confluent-kafka"], ["KAFKA"])}
HOSTS = {"vercel.json": "vercel", "netlify.toml": "netlify", "fly.toml": "fly", "render.yaml": "render", "Procfile": "heroku",
         "app.yaml": "gcp-app-engine", "railway.json": "railway", "railway.toml": "railway", "wrangler.toml": "cloudflare"}
_M = "get|post|put|patch|delete|options|head|all"
_PLANS = "free|starter|basic|pro|premium|business|team|enterprise|growth|plus|standard|scale"
RE_EXPRESS = re.compile(r"\b[A-Za-z_$][\w$]*\.(" + _M + r")\(\s*([\"'`])(/[^\"'`\s]*)\2")
RE_PYROUTE = re.compile(r"@\w+(?:\.\w+)*\.(route|get|post|put|patch|delete)\(\s*(['\"])([^'\"]+)\2([^)]*)\)")
RE_DJANGO = re.compile(r"\b(re_path|path|url)\(\s*r?(['\"])([^'\"]*)\2")
RE_LARAVEL = re.compile(r"Route::(get|post|put|patch|delete|any|options)\(\s*(['\"])([^'\"]+)\2")
RE_RAILS_VERB = re.compile(r"^\s*(get|post|put|patch|delete)\s+(['\"])([^'\"]+)\2", re.M)
RE_RAILS_RES = re.compile(r"^\s*(resources|resource)\s+:(\w+)", re.M)
RE_GO_STD = re.compile(r"\.(?:HandleFunc|Handle)\(\s*\"([^\"]+)\"")
RE_GO_ROUTER = re.compile(r"\b\w+\.(Get|Post|Put|Patch|Delete|GET|POST|PUT|PATCH|DELETE)\(\s*\"(/[^\"]*)\"")
RE_FLAG = re.compile(r"\b(?:isEnabled|isFeatureEnabled|isFeatureFlagEnabled|getFeatureFlag|variation|boolVariation|useFlag|useFeatureFlag|"
                     r"flag_is_active|feature_enabled|feature_flag|is_enabled)\(\s*(?:[\w.]+\s*,\s*)?(['\"])([\w.:-]{1,80})\1")
RE_FLAG_PROP = re.compile(r"\bflags\.([A-Za-z_]\w{1,60})\b")
RE_ENV_JS = re.compile(r"process\.env\.([A-Z][A-Z0-9_]+)|process\.env\[\s*['\"]([A-Z][A-Z0-9_]+)['\"]\s*\]|import\.meta\.env\.([A-Z][A-Z0-9_]+)")
RE_ENV_PY = re.compile(r"os\.environ(?:\.get)?[\[(]\s*['\"]([A-Z][A-Z0-9_]+)['\"]|os\.getenv\(\s*['\"]([A-Z][A-Z0-9_]+)['\"]|"
                       r"\benv\(\s*['\"]([A-Z][A-Z0-9_]+)['\"]|\bENV\[\s*['\"]([A-Z][A-Z0-9_]+)['\"]|"
                       r"os\.Getenv\(\s*\"([A-Z][A-Z0-9_]+)\"|getenv\(\s*['\"]([A-Z][A-Z0-9_]+)['\"]")
RE_PLAN_PRICE = re.compile(r"\b(?:plan|tier|name|id|slug)['\"]?\s*[:=]\s*['\"]([A-Za-z][\w -]{1,30})['\"][\s\S]{0,120}?"
                           r"\b(?:price|amount|monthly\w*|cost)['\"]?\s*[:=]\s*(\d+(?:\.\d{1,2})?)", re.I)
RE_PLAN_KEY = re.compile(r"\b(" + _PLANS + r")['\"]?\s*[:=]\s*\{[^}]{0,120}?\b(?:price|amount|monthly\w*|cost)['\"]?\s*[:=]\s*(\d+(?:\.\d{1,2})?)", re.I)
RE_PLAN_CONST = re.compile(r"\b(" + _PLANS + r")_?(?:monthly_)?price\w*\s*[:=]\s*(\d+(?:\.\d{1,2})?)", re.I)
RE_CURRENCY = re.compile(r"\bcurrency['\"]?\s*[:=]\s*['\"]([A-Za-z]{3})['\"]", re.I)
RE_TEST_FILE = re.compile(r"\.(test|spec)\.[cm]?[jt]sx?$|^test_.*\.py$|_test\.(py|go|rb)$|Test\.php$|_spec\.rb$")
RE_TEST_DIR = re.compile(r"(^|/)(tests?|__tests__|spec)/")
RE_DEP_FILE = re.compile(r"^(package\.json|requirements.*\.txt|pyproject\.toml|Pipfile|go\.mod|composer\.json|Gemfile(\.lock)?|pom\.xml|build\.gradle(\.kts)?)$")


def _glob_re(g):
    s = g.lstrip("/").rstrip("/")
    s = re.sub(r"[.+^${}()|\\]", lambda m: "\\" + m.group(0), s).replace("*", ".*").replace("?", ".")
    return re.compile("^" + s + "$")


def _read(path):
    try:
        with open(path, "r", encoding="latin-1") as f:
            return f.read()
    except OSError:
        return None


def _seg(s):
    if s.startswith("[") and s.endswith("]"):
        return ":" + (re.sub(r"[\[\].]", "", s) or "id")
    return s


def next_route(rel):
    """Next.js file path (relative, posix) -> (route, kind) or None."""
    parts = rel.split("/")
    fname = parts.pop()
    m = re.match(r"^(route|page)\.(?:tsx|ts|jsx|js|mjs)$", fname)
    if m and "app" in parts:
        i = len(parts) - 1 - parts[::-1].index("app")
        segs = [_seg(x) for x in parts[i + 1:] if not (x.startswith("(") and x.endswith(")")) and not x.startswith("@")]
        return "/" + "/".join(segs), m.group(1)
    pm = re.match(r"^(.+)\.(?:tsx|ts|jsx|js)$", fname)
    if pm and "pages" in parts and not pm.group(1).startswith("_"):
        i = len(parts) - 1 - parts[::-1].index("pages")
        segs = [_seg(x) for x in parts[i + 1:]]
        if pm.group(1) != "index":
            segs.append(_seg(pm.group(1)))
        return "/" + "/".join(segs), ("api" if segs and segs[0] == "api" else "page")
    return None


def _parse_deps(name, text, add, gem_lock):
    if name == "package.json":
        try:
            j = json.loads(text)
        except ValueError:
            return
        for k in ("dependencies", "devDependencies", "peerDependencies"):
            for n, v in ((j or {}).get(k) or {}).items():
                add("npm", n, v if isinstance(v, str) else "")
    elif re.match(r"^requirements.*\.txt$", name):
        for line in text.split("\n"):
            t = line.strip()
            if not t or t.startswith("-") or t.startswith("#"):
                continue
            m = re.match(r"^([A-Za-z0-9][A-Za-z0-9_.-]*)(?:\[[^\]]*\])?\s*(?:(==|>=|~=|<=|!=|<|>)\s*([^\s;#,]+))?", t)
            if m:
                add("pypi", m.group(1), (m.group(3) if m.group(2) == "==" else (m.group(2) + m.group(3))) if m.group(3) else "")
    elif name == "pyproject.toml":
        arr = re.search(r"^\s*dependencies\s*=\s*\[([\s\S]*?)\]", text, re.M)
        if arr:
            for m in re.finditer(r"['\"]([A-Za-z0-9][A-Za-z0-9_.-]*)(?:\[[^\]]*\])?\s*([^'\"]*)['\"]", arr.group(1)):
                add("pypi", m.group(1), re.sub(r"^==", "", m.group(2).strip()))
        in_sec = False
        for line in text.split("\n"):
            h = re.match(r"^\s*\[([^\]]+)\]", line)
            if h:
                in_sec = bool(re.search(r"poetry\.(dev-)?dependencies$", h.group(1)) or "dependency-groups" in h.group(1))
                continue
            m = in_sec and re.match(r"^\s*([A-Za-z0-9][A-Za-z0-9_.-]*)\s*=\s*(?:['\"]([^'\"]*)['\"]|\{[^}]*version\s*=\s*['\"]([^'\"]*)['\"])", line)
            if m and m.group(1).lower() != "python":
                add("pypi", m.group(1), m.group(2) or m.group(3) or "")
    elif name == "Pipfile":
        in_sec = False
        for line in text.split("\n"):
            h = re.match(r"^\s*\[([^\]]+)\]", line)
            if h:
                in_sec = bool(re.match(r"^(dev-)?packages$", h.group(1)))
                continue
            m = in_sec and re.match(r"^\s*([A-Za-z0-9][A-Za-z0-9_.-]*)\s*=\s*(?:['\"]([^'\"]*)['\"]|\{[^}]*version\s*=\s*['\"]([^'\"]*)['\"])", line)
            if m:
                v = m.group(2) or m.group(3) or ""
                add("pypi", m.group(1), "" if v == "*" else v)
    elif name == "go.mod":
        block = False
        for line in text.split("\n"):
            t = line.strip()
            if re.match(r"^require\s*\($", t):
                block = True
                continue
            if block and t == ")":
                block = False
                continue
            m = re.match(r"^([\w./~-]+)\s+(v[\w.+-]+)", t) if block else re.match(r"^require\s+([\w./~-]+)\s+(v[\w.+-]+)", t)
            if m:
                add("go", m.group(1), m.group(2))
    elif name == "composer.json":
        try:
            j = json.loads(text)
        except ValueError:
            return
        for k in ("require", "require-dev"):
            for n, v in ((j or {}).get(k) or {}).items():
                if n != "php" and not n.startswith("ext-"):
                    add("composer", n, str(v))
    elif name == "Gemfile":
        for m in re.finditer(r"^\s*gem\s+['\"]([\w.-]+)['\"](?:\s*,\s*['\"]([^'\"]+)['\"])?", text, re.M):
            add("gem", m.group(1), m.group(2) or "")
    elif name == "Gemfile.lock":
        for m in re.finditer(r"^ {4}([\w.-]+) \(([^)]+)\)$", text, re.M):
            gem_lock[m.group(1)] = m.group(2)
    elif name == "pom.xml":
        for m in re.finditer(r"<dependency>([\s\S]*?)</dependency>", text):
            g, a, v = (re.search(p, m.group(1)) for p in (r"<groupId>([^<]+)<", r"<artifactId>([^<]+)<", r"<version>([^<$]+)<"))
            if g and a:
                add("maven", g.group(1).strip() + ":" + a.group(1).strip(), v.group(1).strip() if v else "")
    elif name in ("build.gradle", "build.gradle.kts"):
        for m in re.finditer(r"\b(?:implementation|api|compile|testImplementation|runtimeOnly|compileOnly|kapt)\s*\(?\s*['\"]([^'\":]+:[^'\":]+)(?::[^'\"]*)?['\"]", text):
            add("maven", m.group(1), "")


def _strip_markdown(s):
    s = re.sub(r"```[\s\S]*?```", " ", s)
    s = re.sub(r"!\[[^\]]*\]\([^)]*\)", " ", s)
    s = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", s)
    s = re.sub(r"<[^>]+>", " ", s)
    s = re.sub(r"^[#>\s*+-]+", "", s, flags=re.M)
    s = re.sub(r"[*_`~|]", "", s)
    return re.sub(r"\s+", " ", s).strip()


def _redact(s):
    for r in SECRET_RES:
        s = r.sub("", s)
    return s


def _first(m):
    for g in m.groups():
        if g:
            return g
    return None


def scan(root_dir=".", max_files=MAX_FILES):
    """Scan root_dir and return the manifest dict. Finishes in seconds on a large repo."""
    root = os.path.abspath(str(root_dir or "."))
    languages, deps, routes, route_keys, flags = {}, {}, [], set(), {}
    env_names, mod_dirs, frameworks, ci, hosting, test_fw = set(), {}, set(), set(), set(), set()
    pricing, pricing_keys, next_cands, gem_lock = [], set(), [], {}
    infra = {"docker": False, "compose": False, "kubernetes": False, "terraform": False, "serverless": False}
    sec = {"lockfile": False, "env_committed": False, "secrets_suspected": 0}
    loc = files = test_files = 0
    description = ""

    def add_dep(eco, n, v):
        k = (eco, n)
        if len(deps) < CAPS["dependencies"] * 2 and k not in deps:
            deps[k] = {"ecosystem": eco, "name": n[:200], "version": (v or "")[:100]}

    def add_route(method, p, file):
        if len(routes) >= CAPS["routes"]:
            return
        p = str(p)
        if not p.startswith("/"):
            p = "/" + p
        k = (method, p, file)
        if k in route_keys:
            return
        route_keys.add(k)
        routes.append({"method": method, "path": p[:200], "file": file[:200]})

    def add_env(n):
        if n and len(env_names) < CAPS["env_names"]:
            env_names.add(n)

    gitignore = []
    gi = _read(os.path.join(root, ".gitignore"))
    if gi:
        gitignore = [_glob_re(l.strip()) for l in gi.split("\n") if l.strip() and not l.strip().startswith(("#", "!"))]

    stack = [""]
    done = False
    while stack and not done:
        rel = stack.pop()
        try:
            entries = list(os.scandir(os.path.join(root, rel) if rel else root))
        except OSError:
            continue
        for ent in entries:
            name = ent.name
            relp = rel + "/" + name if rel else name
            try:
                if ent.is_symlink():
                    continue  # never follow symlinks
                if ent.is_dir(follow_symlinks=False):
                    if name in SKIP_DIRS or (name.startswith(".") and name not in KEEP_DOT_DIRS):
                        continue
                    stack.append(relp)
                    continue
                if not ent.is_file(follow_symlinks=False):
                    continue
                size = ent.stat(follow_symlinks=False).st_size
            except OSError:
                continue
            files += 1
            if files > max_files:
                files = max_files
                done = True
                break
            full = os.path.join(root, relp)
            if name in LOCKFILES:
                sec["lockfile"] = True
            if re.match(r"^\.env(\..+)?$", name) and not re.search(r"\.(example|sample|template|dist)$", name):
                if not any(g.match(name) for g in gitignore):
                    sec["env_committed"] = True
                continue  # never read real env files
            ext = name.rsplit(".", 1)[1].lower() if "." in name[1:] else ""
            is_code = ext in CODE_EXT
            is_cfg = ext in CONFIG_EXT or bool(re.match(r"^(Dockerfile|Procfile|Gemfile|Pipfile|Jenkinsfile)", name)) or name.startswith(".env.")
            if name.startswith("Dockerfile"):
                infra["docker"] = True
            if re.match(r"^(docker-compose.*|compose)\.ya?ml$", name):
                infra["compose"] = True
            if ext == "tf":
                infra["terraform"] = True
            if re.match(r"^serverless\.(ya?ml|ts|js)$", name):
                infra["serverless"] = True
            if relp.startswith(".github/workflows/") and re.search(r"\.ya?ml$", name):
                ci.add("github-actions")
            for fname, label in ((".gitlab-ci.yml", "gitlab"), ("Jenkinsfile", "jenkins"), ("azure-pipelines.yml", "azure-pipelines"),
                                 ("bitbucket-pipelines.yml", "bitbucket")):
                if name == fname:
                    ci.add(label)
            if relp == ".circleci/config.yml":
                ci.add("circleci")
            if name in HOSTS:
                hosting.add(HOSTS[name])
            if not (is_code or is_cfg) or size > MAX_BYTES or name in LOCKFILES:
                continue
            if is_code:
                languages[ext] = languages.get(ext, 0) + 1
            text = _read(full)
            if text is None:
                continue
            if is_code:
                loc += text.count("\n") + (1 if text and not text.endswith("\n") else 0)
                parts = relp.split("/")
                if parts[0] in ("src", "apps", "packages", "modules", "features", "app", "lib", "services") and len(parts) > 2:
                    top = parts[0] + "/" + parts[1]
                elif len(parts) > 1:
                    top = parts[0]
                else:
                    top = None
                if top and not top.startswith("."):
                    mod_dirs[top] = mod_dirs.get(top, 0) + 1
            for r in SECRET_RES:
                sec["secrets_suspected"] += len(r.findall(text))  # count only: matches are never stored
            if RE_DEP_FILE.match(name):
                _parse_deps(name, text, add_dep, gem_lock)
            if re.match(r"^(\.env\.(example|sample|template|dist)|example\.env|env\.example)$", name):
                for m in re.finditer(r"^\s*(?:export\s+)?([A-Z][A-Z0-9_]*)\s*=", text, re.M):
                    add_env(m.group(1))
            if ext in ("yml", "yaml") and re.search(r"^kind:\s*(Deployment|Service|Ingress|StatefulSet|DaemonSet|CronJob|Job|ConfigMap)\b", text, re.M) \
                    and re.search(r"^apiVersion:", text, re.M):
                infra["kubernetes"] = True
            if not is_code:
                continue
            is_test = bool(RE_TEST_FILE.search(name)) or (bool(RE_TEST_DIR.search(relp)) and ext in ("js", "jsx", "ts", "tsx", "py", "rb", "php", "go"))
            if is_test:
                test_files += 1
                if ext == "go":
                    test_fw.add("go-test")
                if re.search(r"^(import|from)\s+unittest", text, re.M):
                    test_fw.add("unittest")
                if re.search(r"^\s*(import|from)\s+pytest|\bdef test_", text, re.M):
                    test_fw.add("pytest")
                if re.search(r"\bfrom\s+['\"]vitest['\"]", text):
                    test_fw.add("vitest")
                if "node:test" in text:
                    test_fw.add("node:test")
                elif re.search(r"\bfrom\s+['\"]mocha['\"]", text):
                    test_fw.add("mocha")
                continue
            if ext in JS_EXT:
                if re.search(r"(^|/)(app|pages)/", relp):
                    next_cands.append(relp)
                for m in RE_EXPRESS.finditer(text):
                    add_route(m.group(1).upper(), m.group(3), relp)
                for m in RE_ENV_JS.finditer(text):
                    add_env(_first(m))
            elif ext == "py":
                for m in RE_PYROUTE.finditer(text):
                    methods = [m.group(1).upper()]
                    if m.group(1) == "route":
                        mm = re.search(r"methods\s*=\s*[\[(]([^\])]*)[\])]", m.group(4) or "")
                        methods = [x.upper() for x in re.findall(r"['\"](\w+)['\"]", mm.group(1))] if mm else ["GET"]
                    if m.group(3).startswith("/"):
                        for me in methods:
                            add_route(me, m.group(3), relp)
                if name == "urls.py":
                    for m in RE_DJANGO.finditer(text):
                        p = re.sub(r"^\^", "", m.group(3))
                        p = re.sub(r"\$$", "", p)
                        p = re.sub(r"\(\?P<(\w+)>[^)]*\)", r":\1", p)
                        p = re.sub(r"<(?:\w+:)?(\w+)>", r":\1", p)
                        add_route("ANY", p or "/", relp)
                for m in RE_ENV_PY.finditer(text):
                    add_env(_first(m))
            elif ext == "php":
                for m in RE_LARAVEL.finditer(text):
                    add_route("ANY" if m.group(1) == "any" else m.group(1).upper(), re.sub(r"\{(\w+)\??\}", r":\1", m.group(3)), relp)
                for m in RE_ENV_PY.finditer(text):
                    add_env(_first(m))
            elif ext == "rb":
                if name == "routes.rb":
                    for m in RE_RAILS_VERB.finditer(text):
                        add_route(m.group(1).upper(), m.group(3), relp)
                    for m in RE_RAILS_RES.finditer(text):
                        r = "/" + m.group(2)
                        if m.group(1) == "resources":
                            add_route("GET", r, relp)
                            add_route("POST", r, relp)
                            for v in ("GET", "PUT", "PATCH", "DELETE"):
                                add_route(v, r + "/:id", relp)
                        else:
                            for v in ("GET", "POST", "PUT", "PATCH", "DELETE"):
                                add_route(v, r, relp)
                for m in RE_ENV_PY.finditer(text):
                    add_env(_first(m))
            elif ext == "go":
                for m in RE_GO_STD.finditer(text):
                    mm = re.match(r"^([A-Z]+)\s+(/.*)$", m.group(1))
                    if mm:
                        add_route(mm.group(1), mm.group(2), relp)
                    elif m.group(1).startswith("/"):
                        add_route("ANY", m.group(1), relp)
                for m in RE_GO_ROUTER.finditer(text):
                    add_route(m.group(1).upper(), re.sub(r"\{(\w+)\}", r":\1", m.group(2)), relp)
                for m in RE_ENV_PY.finditer(text):
                    add_env(_first(m))
            for m in RE_FLAG.finditer(text):
                if len(flags) < CAPS["features"]:
                    flags.setdefault(m.group(2), set()).add(relp)
            for m in RE_FLAG_PROP.finditer(text):
                if len(flags) < CAPS["features"]:
                    flags.setdefault(m.group(1), set()).add(relp)
            if len(pricing) < CAPS["pricing_hints"] and not re.search(r"(^|/)(docs?|examples?)/", relp) and re.search(r"price|amount|monthly|cost", text, re.I):
                for rx in (RE_PLAN_KEY, RE_PLAN_CONST, RE_PLAN_PRICE):
                    for m in rx.finditer(text):
                        if len(pricing) >= CAPS["pricing_hints"]:
                            break
                        plan, amount = m.group(1).strip().lower()[:40], float(m.group(2))
                        k = (plan, amount)
                        if not amount > 0 or k in pricing_keys:
                            continue
                        pricing_keys.add(k)
                        cur = RE_CURRENCY.search(text[max(0, m.start() - 200):m.end() + 200])
                        pricing.append({"plan": plan, "amount": int(amount) if amount == int(amount) else amount, "currency": cur.group(1).upper() if cur else ""})

    for d in deps.values():
        if d["ecosystem"] == "gem" and d["name"] in gem_lock:
            d["version"] = gem_lock[d["name"]]
    if gem_lock and not any(d["ecosystem"] == "gem" for d in deps.values()):
        for n, v in gem_lock.items():
            add_dep("gem", n, v)
    dep_list = list(deps.values())[:CAPS["dependencies"]]
    dep_names = [d["name"].lower() for d in dep_list]

    def has_dep(x):
        return any(n == x or n.startswith(x + "/") or n.startswith(x + ":") for n in dep_names)

    for dep, label in FRAMEWORK_DEPS.items():
        if has_dep(dep):
            frameworks.add(label)
    for n, v in (("jest", "jest"), ("vitest", "vitest"), ("mocha", "mocha"), ("pytest", "pytest"), ("phpunit/phpunit", "phpunit"),
                 ("rspec", "rspec"), ("@playwright/test", "playwright"), ("cypress", "cypress")):
        if has_dep(n) or has_dep(n + "-core"):
            test_fw.add(v)
    if "next" in frameworks:
        for rel in next_cands:
            r = next_route(rel)
            if not r:
                continue
            route, kind = r
            if kind == "route":
                text = _read(os.path.join(root, rel)) or ""
                ms = re.findall(r"export\s+(?:async\s+)?(?:function|const)\s+(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)\b", text)
                for m in (ms or ["GET"]):
                    add_route(m, route, rel)
            else:
                add_route("GET", route, rel)
    env_list = sorted(env_names)
    integrations = sorted(n for n, (dp, ev) in INTEGRATIONS.items()
                          if any(s in d for s in dp for d in dep_names) or any(p in e for p in ev for e in env_list))
    features, groups = [], {}
    for r in routes:
        segs = [s for s in r["path"].split("/") if s and not re.match(r"^(api|v\d+|rest|public)$", s, re.I) and not s.startswith((":", "["))]
        if not segs:
            continue
        g = re.sub(r"[^a-z0-9_-]", "", segs[0].lower())
        if g:
            arr = groups.setdefault(g, [])
            if len(arr) < 5:
                arr.append(r["path"])
    for name, paths in groups.items():
        if len(features) < CAPS["features"]:
            features.append({"name": name[:80], "source": "route", "paths": paths})
    for name, st in flags.items():
        if len(features) < CAPS["features"]:
            features.append({"name": name[:80], "source": "flag", "paths": sorted(st)[:5]})
    for d, _n in sorted(mod_dirs.items(), key=lambda kv: -kv[1]):
        if len(features) < CAPS["features"]:
            features.append({"name": d.split("/")[-1][:80], "source": "dir", "paths": [d]})
    for n in ("README.md", "readme.md", "Readme.md", "README", "README.rst", "README.txt"):
        try:
            with open(os.path.join(root, n), "rb") as f:
                description = _redact(_strip_markdown(f.read(2048).decode("utf-8", "ignore")))
            break
        except OSError:
            continue
    infra_out = dict(infra)
    infra_out["ci"] = sorted(ci)
    infra_out["hosting_hints"] = sorted(hosting)
    return {
        "schema": 1, "scanned_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"), "root_name": os.path.basename(root),
        "languages": languages, "loc": loc, "frameworks": sorted(frameworks), "dependencies": dep_list, "routes": routes[:CAPS["routes"]],
        "features": features, "integrations": integrations, "infra": infra_out, "env_names": env_list[:CAPS["env_names"]],
        "pricing_hints": pricing[:CAPS["pricing_hints"]], "description": description,
        "tests": {"files": test_files, "frameworks": sorted(test_fw)}, "security": sec}
