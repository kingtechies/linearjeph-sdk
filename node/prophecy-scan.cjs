"use strict";
/**
 * Prophecy codebase scanner. Reads files locally and returns a manifest (schema 1): names, counts and route
 * templates only. Never source text, never env values, never matched secrets (secrets are only counted).
 * Limits: skips node_modules/.git/vendor/venv/dist/build..., files > 1 MB, symlinks, at most 20,000 files.
 */
const fs = require("node:fs");
const path = require("node:path");

const MAX_FILES = 20000, MAX_BYTES = 1024 * 1024;
const CAPS = { dependencies: 2000, routes: 3000, features: 200, env_names: 500, pricing_hints: 20 };
const SKIP_DIRS = new Set(["node_modules", ".git", "vendor", "venv", ".venv", "env", "dist", "build", "__pycache__", ".next", ".nuxt",
  ".svelte-kit", "target", "coverage", ".tox", ".cache", "site-packages", ".idea", ".vscode", ".gradle", "Pods", "bower_components",
  ".terraform", ".mypy_cache", ".pytest_cache", ".turbo", "out"]);
const KEEP_DOT_DIRS = new Set([".github", ".circleci"]);
const CODE_EXT = new Set(["js", "jsx", "ts", "tsx", "mjs", "cjs", "py", "go", "php", "rb", "java", "kt", "rs", "cs", "swift", "dart",
  "vue", "svelte", "scala", "ex", "exs", "c", "cpp", "h", "sh", "sql", "html", "css", "scss"]);
const CONFIG_EXT = new Set(["json", "yml", "yaml", "toml", "ini", "cfg", "xml", "tf", "properties", "gradle", "kts", "mod", "env", "md", "txt"]);
const LOCKFILES = new Set(["package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "poetry.lock", "Pipfile.lock", "uv.lock",
  "go.sum", "composer.lock", "Gemfile.lock"]);
const SECRET_RES = [
  /\bAKIA[0-9A-Z]{16}\b/g, /\bsk_live_[0-9A-Za-z]{16,}/g, /\bghp_[A-Za-z0-9]{30,}/g, /\bgithub_pat_[A-Za-z0-9_]{40,}/g,
  /-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----/g, /\bxox[baprs]-[A-Za-z0-9-]{10,}/g,
  /\bAIza[0-9A-Za-z_-]{35}/g, /\bljp_sk_[A-Za-z0-9_-]{20,}/g,
];
const FRAMEWORK_DEPS = {
  express: "express", fastify: "fastify", koa: "koa", next: "next", "@nestjs/core": "nestjs", react: "react", vue: "vue", nuxt: "nuxt",
  svelte: "svelte", "@sveltejs/kit": "sveltekit", "@angular/core": "angular", "@hapi/hapi": "hapi", "@remix-run/node": "remix", astro: "astro",
  django: "django", flask: "flask", fastapi: "fastapi", starlette: "starlette", tornado: "tornado", aiohttp: "aiohttp", celery: "celery",
  "laravel/framework": "laravel", "symfony/framework-bundle": "symfony", rails: "rails", sinatra: "sinatra",
  "github.com/gin-gonic/gin": "gin", "github.com/go-chi/chi": "chi", "github.com/labstack/echo": "echo", "github.com/gofiber/fiber": "fiber",
  "org.springframework.boot": "spring-boot", flutter: "flutter",
};
const INTEGRATIONS = {
  stripe: { dep: ["stripe"], env: ["STRIPE"] }, paddle: { dep: ["paddle"], env: ["PADDLE"] }, paystack: { dep: ["paystack"], env: ["PAYSTACK"] },
  flutterwave: { dep: ["flutterwave"], env: ["FLUTTERWAVE", "FLW_"] }, bachs: { dep: ["bachs"], env: ["BACHS"] },
  auth0: { dep: ["auth0"], env: ["AUTH0"] }, clerk: { dep: ["@clerk", "clerk"], env: ["CLERK"] },
  supabase: { dep: ["supabase"], env: ["SUPABASE"] }, firebase: { dep: ["firebase"], env: ["FIREBASE"] },
  sentry: { dep: ["sentry"], env: ["SENTRY"] }, segment: { dep: ["@segment", "analytics-node", "segment"], env: ["SEGMENT"] },
  mixpanel: { dep: ["mixpanel"], env: ["MIXPANEL"] }, posthog: { dep: ["posthog"], env: ["POSTHOG"] },
  amplitude: { dep: ["amplitude"], env: ["AMPLITUDE"] }, launchdarkly: { dep: ["launchdarkly"], env: ["LAUNCHDARKLY", "LD_SDK"] },
  unleash: { dep: ["unleash"], env: ["UNLEASH"] }, twilio: { dep: ["twilio"], env: ["TWILIO"] },
  sendgrid: { dep: ["sendgrid"], env: ["SENDGRID"] }, resend: { dep: ["resend"], env: ["RESEND"] }, mailgun: { dep: ["mailgun"], env: ["MAILGUN"] },
  postmark: { dep: ["postmark"], env: ["POSTMARK"] }, openai: { dep: ["openai"], env: ["OPENAI"] }, anthropic: { dep: ["anthropic"], env: ["ANTHROPIC"] },
  s3: { dep: ["@aws-sdk/client-s3", "aws-sdk", "boto3", "aws/aws-sdk-php", "aws-sdk-s3"], env: ["AWS_S3", "S3_", "AWS_ACCESS"] },
  cloudinary: { dep: ["cloudinary"], env: ["CLOUDINARY"] }, algolia: { dep: ["algolia"], env: ["ALGOLIA"] },
  redis: { dep: ["redis", "ioredis"], env: ["REDIS"] }, rabbitmq: { dep: ["amqplib", "pika"], env: ["RABBITMQ", "AMQP"] },
  kafka: { dep: ["kafkajs", "kafka-python", "confluent-kafka"], env: ["KAFKA"] },
};
const METHODS = "get|post|put|patch|delete|options|head|all";
const RE = {
  express: new RegExp("\\b[A-Za-z_$][\\w$]*\\.(" + METHODS + ")\\(\\s*([\"'`])(\\/[^\"'`\\s]*)\\2", "g"),
  pyRoute: /@\w+(?:\.\w+)*\.(route|get|post|put|patch|delete)\(\s*(['"])([^'"]+)\2([^)]*)\)/g,
  djangoPath: /\b(re_path|path|url)\(\s*r?(['"])([^'"]*)\2/g,
  laravel: /Route::(get|post|put|patch|delete|any|options)\(\s*(['"])([^'"]+)\2/g,
  railsVerb: /^\s*(get|post|put|patch|delete)\s+(['"])([^'"]+)\2/gm,
  railsRes: /^\s*(resources|resource)\s+:(\w+)/gm,
  goStd: /\.(?:HandleFunc|Handle)\(\s*"([^"]+)"/g,
  goRouter: /\b\w+\.(Get|Post|Put|Patch|Delete|GET|POST|PUT|PATCH|DELETE)\(\s*"(\/[^"]*)"/g,
  flag: /\b(?:isEnabled|isFeatureEnabled|isFeatureFlagEnabled|getFeatureFlag|variation|boolVariation|useFlag|useFeatureFlag|flag_is_active|feature_enabled|feature_flag|is_enabled)\(\s*(?:[\w.]+\s*,\s*)?(['"])([\w.:-]{1,80})\1/g,
  flagProp: /\bflags\.([A-Za-z_]\w{1,60})\b/g,
  envJs: /process\.env\.([A-Z][A-Z0-9_]+)|process\.env\[\s*['"]([A-Z][A-Z0-9_]+)['"]\s*\]|import\.meta\.env\.([A-Z][A-Z0-9_]+)/g,
  envPy: /os\.environ(?:\.get)?[\[(]\s*['"]([A-Z][A-Z0-9_]+)['"]|os\.getenv\(\s*['"]([A-Z][A-Z0-9_]+)['"]|\benv\(\s*['"]([A-Z][A-Z0-9_]+)['"]|\bENV\[\s*['"]([A-Z][A-Z0-9_]+)['"]|os\.Getenv\(\s*"([A-Z][A-Z0-9_]+)"|getenv\(\s*['"]([A-Z][A-Z0-9_]+)['"]/g,
  planPrice: /\b(?:plan|tier|name|id|slug)['"]?\s*[:=]\s*['"]([A-Za-z][\w -]{1,30})['"][\s\S]{0,120}?\b(?:price|amount|monthly\w*|cost)['"]?\s*[:=]\s*(\d+(?:\.\d{1,2})?)/gi,
  planKey: /\b(free|starter|basic|pro|premium|business|team|enterprise|growth|plus|standard|scale)['"]?\s*[:=]\s*\{[^}]{0,120}?\b(?:price|amount|monthly\w*|cost)['"]?\s*[:=]\s*(\d+(?:\.\d{1,2})?)/gi,
  planConst: /\b(free|starter|basic|pro|premium|business|team|enterprise|growth|plus|standard|scale)_?(?:monthly_)?price\w*\s*[:=]\s*(\d+(?:\.\d{1,2})?)/gi,
  currency: /\bcurrency['"]?\s*[:=]\s*['"]([A-Za-z]{3})['"]/i,
};

const clip = (s, n) => String(s).slice(0, n);
function globToRe(g) {
  const s = g.replace(/^\//, "").replace(/\/$/, "").replace(/[.+^${}()|\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".");
  return new RegExp("^" + s + "$");
}
function readText(p) { try { return fs.readFileSync(p, "latin1"); } catch (_) { return null; } }

function normSegment(s) {
  if (s.startsWith("[") && s.endsWith("]")) { const n = s.replace(/[\[\].]/g, ""); return ":" + (n || "id"); }
  return s;
}
/** Next.js file path (relative, posix) -> {route, kind} or null. */
function nextRoute(rel) {
  const parts = rel.split("/");
  const file = parts.pop();
  const m = /^(route|page)\.(?:tsx|ts|jsx|js|mjs)$/.exec(file);
  let i = parts.lastIndexOf("app");
  if (m && i >= 0) {
    const segs = parts.slice(i + 1).filter((x) => !(x.startsWith("(") && x.endsWith(")")) && !x.startsWith("@")).map(normSegment);
    return { route: "/" + segs.join("/"), kind: m[1] };
  }
  i = parts.lastIndexOf("pages");
  const pm = /^(.+)\.(?:tsx|ts|jsx|js)$/.exec(file);
  if (pm && i >= 0 && !pm[1].startsWith("_")) {
    const segs = parts.slice(i + 1).map(normSegment);
    if (pm[1] !== "index") segs.push(normSegment(pm[1]));
    return { route: "/" + segs.join("/"), kind: segs[0] === "api" ? "api" : "page" };
  }
  return null;
}

function parseDeps(name, text, add, state) {
  const base = path.basename(name);
  if (base === "package.json") {
    let j; try { j = JSON.parse(text); } catch (_) { return; }
    for (const k of ["dependencies", "devDependencies", "peerDependencies"]) {
      for (const [n, v] of Object.entries((j && j[k]) || {})) add("npm", n, typeof v === "string" ? v : "");
    }
  } else if (/^requirements.*\.txt$/.test(base)) {
    for (const line of text.split("\n")) {
      const m = /^\s*([A-Za-z0-9][A-Za-z0-9_.-]*)(?:\[[^\]]*\])?\s*(?:(==|>=|~=|<=|!=|<|>)\s*([^\s;#,]+))?/.exec(line);
      if (m && !line.trim().startsWith("-") && !line.trim().startsWith("#")) add("pypi", m[1], m[3] ? (m[2] === "==" ? m[3] : m[2] + m[3]) : "");
    }
  } else if (base === "pyproject.toml") {
    const arr = /^\s*dependencies\s*=\s*\[([\s\S]*?)\]/m.exec(text);
    if (arr) for (const m of arr[1].matchAll(/['"]([A-Za-z0-9][A-Za-z0-9_.-]*)(?:\[[^\]]*\])?\s*([^'"]*)['"]/g)) add("pypi", m[1], m[2].trim().replace(/^==/, ""));
    let inSec = false;
    for (const line of text.split("\n")) {
      const h = /^\s*\[([^\]]+)\]/.exec(line);
      if (h) { inSec = /poetry\.(dev-)?dependencies$/.test(h[1]) || /dependency-groups/.test(h[1]); continue; }
      const m = inSec && /^\s*([A-Za-z0-9][A-Za-z0-9_.-]*)\s*=\s*(?:['"]([^'"]*)['"]|\{[^}]*version\s*=\s*['"]([^'"]*)['"])/.exec(line);
      if (m && m[1].toLowerCase() !== "python") add("pypi", m[1], m[2] || m[3] || "");
    }
  } else if (base === "Pipfile") {
    let inSec = false;
    for (const line of text.split("\n")) {
      const h = /^\s*\[([^\]]+)\]/.exec(line);
      if (h) { inSec = /^(dev-)?packages$/.test(h[1]); continue; }
      const m = inSec && /^\s*([A-Za-z0-9][A-Za-z0-9_.-]*)\s*=\s*(?:['"]([^'"]*)['"]|\{[^}]*version\s*=\s*['"]([^'"]*)['"])/.exec(line);
      if (m) add("pypi", m[1], (m[2] || m[3] || "").replace(/^\*$/, ""));
    }
  } else if (base === "go.mod") {
    let block = false;
    for (const line of text.split("\n")) {
      const t = line.trim();
      if (/^require\s*\($/.test(t)) { block = true; continue; }
      if (block && t === ")") { block = false; continue; }
      const m = block ? /^([\w./~-]+)\s+(v[\w.+-]+)/.exec(t) : /^require\s+([\w./~-]+)\s+(v[\w.+-]+)/.exec(t);
      if (m) add("go", m[1], m[2]);
    }
  } else if (base === "composer.json") {
    let j; try { j = JSON.parse(text); } catch (_) { return; }
    for (const k of ["require", "require-dev"]) for (const [n, v] of Object.entries((j && j[k]) || {})) { if (n !== "php" && !n.startsWith("ext-")) add("composer", n, String(v)); }
  } else if (base === "Gemfile") {
    for (const m of text.matchAll(/^\s*gem\s+['"]([\w.-]+)['"](?:\s*,\s*['"]([^'"]+)['"])?/gm)) add("gem", m[1], m[2] || "");
  } else if (base === "Gemfile.lock") {
    for (const m of text.matchAll(/^ {4}([\w.-]+) \(([^)]+)\)$/gm)) state.gemLock.set(m[1], m[2]);
  } else if (base === "pom.xml") {
    for (const m of text.matchAll(/<dependency>([\s\S]*?)<\/dependency>/g)) {
      const g = /<groupId>([^<]+)</.exec(m[1]), a = /<artifactId>([^<]+)</.exec(m[1]), v = /<version>([^<$]+)</.exec(m[1]);
      if (g && a) add("maven", g[1].trim() + ":" + a[1].trim(), v ? v[1].trim() : "");
    }
  } else if (base === "build.gradle" || base === "build.gradle.kts") {
    for (const m of text.matchAll(/\b(?:implementation|api|compile|testImplementation|runtimeOnly|compileOnly|kapt)\s*\(?\s*['"]([^'":]+:[^'":]+)(?::[^'"]*)?['"]/g)) add("maven", m[1], "");
  }
}

function stripMarkdown(s) {
  return s.replace(/```[\s\S]*?```/g, " ").replace(/!\[[^\]]*\]\([^)]*\)/g, " ").replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/<[^>]+>/g, " ").replace(/^[#>\s*+-]+/gm, "").replace(/[*_`~|]/g, "").replace(/\s+/g, " ").trim();
}
function redactSecrets(s) { for (const re of SECRET_RES) s = s.replace(re, ""); return s; }

/** Scan rootDir and return the manifest. Synchronous; finishes in seconds on a large repo. */
function scan(rootDir, opts = {}) {
  const root = path.resolve(String(rootDir || "."));
  const maxFiles = opts.maxFiles || MAX_FILES;
  const languages = {}, deps = new Map(), routes = [], routeKeys = new Set(), flags = new Map(), envNames = new Set();
  const modDirs = new Map(), frameworks = new Set(), ci = new Set(), hosting = new Set(), testFw = new Set(), pricing = [], pricingKeys = new Set();
  const infra = { docker: false, compose: false, kubernetes: false, terraform: false, serverless: false };
  const state = { gemLock: new Map() };
  const nextCands = [];
  const sec = { lockfile: false, env_committed: false, secrets_suspected: 0 };
  const envFiles = [];
  let loc = 0, files = 0, testFiles = 0, description = "", gitignore = [];

  const addDep = (eco, n, v) => { if (deps.size < CAPS.dependencies * 2) { const k = eco + "\0" + n; if (!deps.has(k)) deps.set(k, { ecosystem: eco, name: clip(n, 200), version: clip(v || "", 100) }); } };
  const addRoute = (method, p, file) => {
    if (routes.length >= CAPS.routes) return;
    p = String(p); if (!p.startsWith("/")) p = "/" + p;
    const k = method + " " + p + " " + file; if (routeKeys.has(k)) return; routeKeys.add(k);
    routes.push({ method, path: clip(p, 200), file: clip(file, 200) });
  };
  const addEnv = (n) => { if (envNames.size < CAPS.env_names) envNames.add(n); };

  const gi = readText(path.join(root, ".gitignore"));
  if (gi) gitignore = gi.split("\n").map((l) => l.trim()).filter((l) => l && !l.startsWith("#") && !l.startsWith("!")).map(globToRe);

  const stack = [""];
  outer: while (stack.length) {
    const rel = stack.pop();
    let entries;
    try { entries = fs.readdirSync(path.join(root, rel), { withFileTypes: true }); } catch (_) { continue; }
    for (const ent of entries) {
      const name = ent.name, relp = rel ? rel + "/" + name : name;
      if (ent.isSymbolicLink()) continue; // never follow symlinks
      if (ent.isDirectory()) {
        if (SKIP_DIRS.has(name) || (name.startsWith(".") && !KEEP_DOT_DIRS.has(name))) continue;
        stack.push(relp); continue;
      }
      if (!ent.isFile()) continue;
      if (++files > maxFiles) { files = maxFiles; break outer; }
      const full = path.join(root, relp);
      let size; try { size = fs.lstatSync(full).size; } catch (_) { continue; }
      if (LOCKFILES.has(name)) sec.lockfile = true;
      if (/^\.env(\..+)?$/.test(name) && !/\.(example|sample|template|dist)$/.test(name)) {
        if (!gitignore.some((re) => re.test(name))) sec.env_committed = true;
        continue; // never read real env files
      }
      const dot = name.lastIndexOf(".");
      const ext = dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
      const isCode = CODE_EXT.has(ext), isCfg = CONFIG_EXT.has(ext) || /^(Dockerfile|Procfile|Gemfile|Pipfile|Jenkinsfile)/.test(name) || /^\.env\./.test(name);
      // infra markers that need no content
      if (/^Dockerfile/.test(name)) infra.docker = true;
      if (/^(docker-compose.*|compose)\.ya?ml$/.test(name)) infra.compose = true;
      if (ext === "tf") infra.terraform = true;
      if (/^serverless\.(ya?ml|ts|js)$/.test(name)) infra.serverless = true;
      if (/^\.github\/workflows\//.test(relp) && /\.ya?ml$/.test(name)) ci.add("github-actions");
      if (name === ".gitlab-ci.yml") ci.add("gitlab");
      if (relp === ".circleci/config.yml") ci.add("circleci");
      if (name === "Jenkinsfile") ci.add("jenkins");
      if (name === "azure-pipelines.yml") ci.add("azure-pipelines");
      if (name === "bitbucket-pipelines.yml") ci.add("bitbucket");
      const hostMap = { "vercel.json": "vercel", "netlify.toml": "netlify", "fly.toml": "fly", "render.yaml": "render", Procfile: "heroku", "app.yaml": "gcp-app-engine", "railway.json": "railway", "railway.toml": "railway", "wrangler.toml": "cloudflare" };
      if (hostMap[name]) hosting.add(hostMap[name]);
      if (!isCode && !isCfg) continue;
      if (size > MAX_BYTES) continue;
      if (LOCKFILES.has(name)) continue;
      if (isCode) languages[ext] = (languages[ext] || 0) + 1;
      const text = readText(full);
      if (text == null) continue;
      if (isCode) {
        let n = 0; for (let i = text.indexOf("\n"); i !== -1; i = text.indexOf("\n", i + 1)) n++;
        loc += n + (text.length && !text.endsWith("\n") ? 1 : 0);
        const parts = relp.split("/");
        const top = ["src", "apps", "packages", "modules", "features", "app", "lib", "services"].includes(parts[0]) && parts.length > 2 ? parts[0] + "/" + parts[1] : parts.length > 1 ? parts[0] : null;
        if (top && !top.startsWith(".")) modDirs.set(top, (modDirs.get(top) || 0) + 1);
      }
      // secrets: count only
      for (const re of SECRET_RES) { re.lastIndex = 0; const m = text.match(re); if (m) sec.secrets_suspected += m.length; }
      // dependencies
      if (/^(package\.json|requirements.*\.txt|pyproject\.toml|Pipfile|go\.mod|composer\.json|Gemfile(\.lock)?|pom\.xml|build\.gradle(\.kts)?)$/.test(name)) parseDeps(name, text, addDep, state);
      // env names from example files
      if (/^(\.env\.(example|sample|template|dist)|example\.env|env\.example)$/.test(name)) {
        for (const m of text.matchAll(/^\s*(?:export\s+)?([A-Z][A-Z0-9_]*)\s*=/gm)) addEnv(m[1]);
      }
      if (ext === "yml" || ext === "yaml") {
        if (/^kind:\s*(Deployment|Service|Ingress|StatefulSet|DaemonSet|CronJob|Job|ConfigMap)\b/m.test(text) && /^apiVersion:/m.test(text)) infra.kubernetes = true;
      }
      if (!isCode) continue;
      // tests
      const isTest = /\.(test|spec)\.[cm]?[jt]sx?$/.test(name) || /^test_.*\.py$/.test(name) || /_test\.(py|go|rb)$/.test(name) || /Test\.php$/.test(name) || /_spec\.rb$/.test(name)
        || (/(^|\/)(tests?|__tests__|spec)\//.test(relp) && ["js", "jsx", "ts", "tsx", "py", "rb", "php", "go"].includes(ext));
      if (isTest) {
        testFiles++;
        if (ext === "go") testFw.add("go-test");
        if (/^(import|from)\s+unittest|^import unittest/m.test(text)) testFw.add("unittest");
        if (/^\s*(import|from)\s+pytest|\bdef test_/m.test(text)) testFw.add("pytest");
        if (/\bfrom\s+['"]vitest['"]/.test(text)) testFw.add("vitest");
        if (/node:test|\bfrom\s+['"]mocha['"]/.test(text)) testFw.add(/node:test/.test(text) ? "node:test" : "mocha");
        continue; // test code is not product surface
      }
      // routes / flags / env references
      if (["js", "jsx", "ts", "tsx", "mjs", "cjs"].includes(ext)) {
        if (/(^|\/)(app|pages)\//.test(relp)) nextCands.push(relp);
        for (const m of text.matchAll(RE.express)) addRoute(m[1].toUpperCase(), m[3], relp);
        for (const m of text.matchAll(RE.envJs)) addEnv(m[1] || m[2] || m[3]);
      } else if (ext === "py") {
        for (const m of text.matchAll(RE.pyRoute)) {
          let methods = [m[1].toUpperCase()];
          if (m[1] === "route") { const mm = /methods\s*=\s*[\[(]([^\])]*)[\])]/.exec(m[4] || ""); methods = mm ? [...mm[1].matchAll(/['"](\w+)['"]/g)].map((x) => x[1].toUpperCase()) : ["GET"]; }
          if (m[3].startsWith("/")) for (const me of methods) addRoute(me, m[3], relp);
        }
        if (name === "urls.py") for (const m of text.matchAll(RE.djangoPath)) addRoute("ANY", m[3].replace(/^\^/, "").replace(/\$$/, "").replace(/\(\?P<(\w+)>[^)]*\)/g, ":$1").replace(/<(?:\w+:)?(\w+)>/g, ":$1") || "/", relp);
        for (const m of text.matchAll(RE.envPy)) addEnv(m[1] || m[2] || m[3] || m[4] || m[5] || m[6]);
      } else if (ext === "php") {
        for (const m of text.matchAll(RE.laravel)) addRoute(m[1] === "any" ? "ANY" : m[1].toUpperCase(), m[3].replace(/\{(\w+)\??\}/g, ":$1"), relp);
        for (const m of text.matchAll(RE.envPy)) addEnv(m[1] || m[2] || m[3] || m[4] || m[5] || m[6]);
      } else if (ext === "rb") {
        if (name === "routes.rb") {
          for (const m of text.matchAll(RE.railsVerb)) addRoute(m[1].toUpperCase(), m[3].replace(/:(\w+)/g, ":$1"), relp);
          for (const m of text.matchAll(RE.railsRes)) {
            const r = "/" + m[2];
            if (m[1] === "resources") { addRoute("GET", r, relp); addRoute("POST", r, relp); for (const v of ["GET", "PUT", "PATCH", "DELETE"]) addRoute(v, r + "/:id", relp); }
            else for (const v of ["GET", "POST", "PUT", "PATCH", "DELETE"]) addRoute(v, r, relp);
          }
        }
        for (const m of text.matchAll(RE.envPy)) addEnv(m[1] || m[2] || m[3] || m[4] || m[5] || m[6]);
      } else if (ext === "go") {
        for (const m of text.matchAll(RE.goStd)) { const mm = /^([A-Z]+)\s+(\/.*)$/.exec(m[1]); if (mm) addRoute(mm[1], mm[2], relp); else if (m[1].startsWith("/")) addRoute("ANY", m[1], relp); }
        for (const m of text.matchAll(RE.goRouter)) addRoute(m[1].toUpperCase(), m[2].replace(/\{(\w+)\}/g, ":$1"), relp);
        for (const m of text.matchAll(RE.envPy)) addEnv(m[1] || m[2] || m[3] || m[4] || m[5] || m[6]);
      }
      for (const m of text.matchAll(RE.flag)) if (flags.size < CAPS.features) { const f = flags.get(m[2]) || new Set(); f.add(relp); flags.set(m[2], f); }
      for (const m of text.matchAll(RE.flagProp)) if (flags.size < CAPS.features) { const f = flags.get(m[1]) || new Set(); f.add(relp); flags.set(m[1], f); }
      // pricing hints
      if (pricing.length < CAPS.pricing_hints && !/(^|\/)(node_modules|docs?|examples?)\//.test(relp) && /price|amount|monthly|cost/i.test(text)) {
        for (const re of [RE.planKey, RE.planConst, RE.planPrice]) {
          for (const m of text.matchAll(re)) {
            if (pricing.length >= CAPS.pricing_hints) break;
            const plan = clip(m[1].trim().toLowerCase(), 40), amount = Number(m[2]);
            const k = plan + ":" + amount;
            if (!(amount > 0) || pricingKeys.has(k)) continue;
            pricingKeys.add(k);
            const cur = RE.currency.exec(text.slice(Math.max(0, m.index - 200), m.index + m[0].length + 200));
            pricing.push({ plan, amount, currency: cur ? cur[1].toUpperCase() : "" });
          }
        }
      }
    }
  }

  // fill gem versions from Gemfile.lock
  for (const d of deps.values()) if (d.ecosystem === "gem" && state.gemLock.has(d.name)) d.version = state.gemLock.get(d.name);
  if (![...deps.values()].some((d) => d.ecosystem === "gem") && state.gemLock.size) for (const [n, v] of state.gemLock) addDep("gem", n, v);
  const depList = [...deps.values()].slice(0, CAPS.dependencies);
  // frameworks
  const depNames = depList.map((d) => d.name.toLowerCase());
  const hasDep = (x) => depNames.some((n) => n === x || n.startsWith(x + "/") || n.startsWith(x + ":"));
  for (const [dep, label] of Object.entries(FRAMEWORK_DEPS)) if (hasDep(dep)) frameworks.add(label);
  for (const [n, v] of [["jest", "jest"], ["vitest", "vitest"], ["mocha", "mocha"], ["pytest", "pytest"], ["phpunit/phpunit", "phpunit"], ["rspec", "rspec"], ["@playwright/test", "playwright"], ["cypress", "cypress"]]) if (hasDep(n) || hasDep(n + "-core")) testFw.add(v);
  // Next.js routes
  if (frameworks.has("next")) {
    for (const rel of nextCands) {
      const r = nextRoute(rel);
      if (!r) continue;
      if (r.kind === "route") {
        const text = readText(path.join(root, rel)) || "";
        const ms = [...text.matchAll(/export\s+(?:async\s+)?(?:function|const)\s+(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)\b/g)].map((m) => m[1]);
        for (const m of ms.length ? ms : ["GET"]) addRoute(m, r.route, rel);
      } else addRoute("GET", r.route, rel);
    }
  }
  // integrations
  const integrations = [];
  const envList = [...envNames];
  for (const [name, spec] of Object.entries(INTEGRATIONS)) {
    if (spec.dep.some((s) => depNames.some((n) => n.includes(s))) || spec.env.some((p) => envList.some((e) => e.includes(p)))) integrations.push(name);
  }
  // features
  const features = [];
  const groups = new Map();
  for (const r of routes) {
    const segs = r.path.split("/").filter(Boolean).filter((s) => !/^(api|v\d+|rest|public)$/i.test(s) && !s.startsWith(":") && !s.startsWith("["));
    if (!segs.length) continue;
    const g = segs[0].toLowerCase().replace(/[^a-z0-9_-]/g, "");
    if (!g) continue;
    const arr = groups.get(g) || []; if (arr.length < 5) arr.push(r.path); groups.set(g, arr);
  }
  for (const [name, paths] of groups) if (features.length < CAPS.features) features.push({ name: clip(name, 80), source: "route", paths });
  for (const [name, set] of flags) if (features.length < CAPS.features) features.push({ name: clip(name, 80), source: "flag", paths: [...set].slice(0, 5) });
  for (const [dir] of [...modDirs].sort((a, b) => b[1] - a[1])) if (features.length < CAPS.features) features.push({ name: clip(dir.split("/").pop(), 80), source: "dir", paths: [dir] });
  // description
  for (const n of ["README.md", "readme.md", "Readme.md", "README", "README.rst", "README.txt"]) {
    try {
      const fd = fs.openSync(path.join(root, n), "r"); const buf = Buffer.alloc(2048); const k = fs.readSync(fd, buf, 0, 2048, 0); fs.closeSync(fd);
      description = redactSecrets(stripMarkdown(buf.slice(0, k).toString("utf8"))); break;
    } catch (_) { /* try next */ }
  }
  return {
    schema: 1, scanned_at: new Date().toISOString(), root_name: path.basename(root), languages, loc,
    frameworks: [...frameworks].sort(), dependencies: depList, routes: routes.slice(0, CAPS.routes), features,
    integrations: integrations.sort(), infra: { ...infra, ci: [...ci].sort(), hosting_hints: [...hosting].sort() },
    env_names: envList.sort().slice(0, CAPS.env_names), pricing_hints: pricing.slice(0, CAPS.pricing_hints), description,
    tests: { files: testFiles, frameworks: [...testFw].sort() }, security: sec,
  };
}
module.exports = { scan, nextRoute, MAX_FILES, MAX_BYTES };
