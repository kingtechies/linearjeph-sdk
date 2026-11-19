const test = require("node:test");
const assert = require("node:assert");
const http = require("node:http");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const zlib = require("node:zlib");
const crypto = require("node:crypto");
const { EventEmitter } = require("node:events");
const lj = require("../index.cjs");
const prophecy = require("../prophecy.cjs");
const { parseCsv } = require("../bin/linearjeph.cjs");

const sha = (s) => crypto.createHash("sha256").update(String(s)).digest("hex");

/** Local stand-in: `script` = [status, headers, body] answered first, then 202 (ingest) or 200 {accepted:n} (api). */
function server(script = []) {
  const got = [], hits = [], queue = [...script];
  const srv = http.createServer((req, res) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      let raw = Buffer.concat(chunks), gz = req.headers["content-encoding"] === "gzip";
      if (gz) raw = zlib.gunzipSync(raw);
      const body = raw.length ? JSON.parse(raw.toString()) : null;
      if (queue.length) { const [code, headers] = queue.shift(); hits.push(code); res.writeHead(code, headers || {}); return res.end("{}"); }
      hits.push(200);
      got.push({ method: req.method, path: req.url, headers: req.headers, gz, body });
      const ingest = req.url.startsWith("/ingest/");
      res.writeHead(ingest ? 202 : 200, { "Content-Type": "application/json" });
      if (req.url.startsWith("/api/v1/prophecy/sdk/import")) return res.end(JSON.stringify({ job: got.length, accepted: body.rows.length, rejected: 0, errors: [] }));
      if (req.url.startsWith("/api/v1/prophecy/sdk/embed")) return res.end(JSON.stringify({ url: "https://x/prophecy-embed/tok", expires_at: "z" }));
      res.end(JSON.stringify({ accepted: 1, ok: true }));
    });
  });
  return new Promise((r) => srv.listen(0, "127.0.0.1", () => r({ srv, got, hits, url: `http://127.0.0.1:${srv.address().port}` })));
}
const evs = (s) => s.got.filter((g) => g.body && g.body.events).flatMap((g) => g.body.events);
async function setup(script, extra = {}) {
  const s = await server(script);
  prophecy.init({ ingestKey: "lj_app_k", secretKey: "ljp_sk_" + "a".repeat(32), endpoint: s.url, handlers: false, backoffMs: 5, flushIntervalMs: 60000, ...extra });
  return s;
}
const done = async (s) => { await prophecy.close(); s.srv.close(); };

test("exported from the main package and the subpath", () => {
  assert.strictEqual(lj.prophecy, prophecy);
  assert.equal(lj.SDK_VERSION, "1.2.0");
  for (const f of ["init", "track", "feature", "signup", "payment", "subscription", "churn", "identify", "middleware", "scan", "sendManifest", "importHistory", "report", "refresh", "live", "embedUrl", "flush", "close"]) assert.equal(typeof prophecy[f], "function", f);
});

test("track batches, gzips, hashes uids and sends the ingest key", async () => {
  const s = await setup();
  prophecy.track("exported_report", { uid: "user-1", props: { rows: 3 } });
  prophecy.feature("search", "user-1");
  prophecy.signup("user-2", { plan: "pro", channel: "ads", country: "NG" });
  prophecy.payment("user-2", 29, "USD", { plan: "pro" });
  prophecy.payment("user-2", -29);
  prophecy.subscription("user-2", { plan: "pro", mrr: 29, status: "active" });
  prophecy.churn("user-2", "too expensive");
  prophecy.identify("user-3", { plan: "free", country: "GH", shoe_size: 9 });
  assert.ok(await prophecy.flush());
  const e = evs(s);
  assert.equal(e.length, 7);
  const post = s.got.find((g) => g.path === "/ingest/v1/prophecy/events");
  assert.ok(post.gz, "gzip body"); assert.equal(post.headers["x-lj-key"], "lj_app_k"); assert.equal(post.method, "POST");
  assert.equal(e[0].uid, sha("user-1")); assert.equal(e[0].kind, "custom"); assert.deepEqual(e[0].props, { rows: 3 });
  assert.equal(e[1].kind, "feature"); assert.equal(e[1].feature, "search");
  assert.equal(e[3].kind, "payment"); assert.equal(e[3].amount, 29); assert.equal(e[3].currency, "USD");
  assert.equal(e[4].kind, "refund"); assert.equal(e[4].amount, -29);
  assert.equal(e[5].kind, "subscription"); assert.equal(e[5].props.mrr, 29);
  assert.equal(e[6].kind, "churn");
  const custs = s.got.filter((g) => g.path === "/ingest/v1/prophecy/identify").flatMap((g) => g.body.customers);
  const c3 = custs.find((c) => c.uid === sha("user-3"));
  assert.equal(c3.plan, "free"); assert.deepEqual(c3.traits, { shoe_size: 9 });
  assert.ok(custs.some((c) => c.uid === sha("user-2") && c.signup_at && c.country === "NG"));
  assert.ok(!JSON.stringify(s.got).includes("user-1"), "raw uid never sent");
  await done(s);
});

test("hashUserIds false keeps ids", async () => {
  const s = await setup([], { hashUserIds: false });
  prophecy.track("x", { uid: "raw-id" });
  await prophecy.flush();
  assert.equal(evs(s)[0].uid, "raw-id");
  await done(s);
});

test("batches of 100 per request", async () => {
  const s = await setup();
  for (let i = 0; i < 250; i++) prophecy.track("e" + i);
  await prophecy.flush();
  const sizes = s.got.filter((g) => g.body.events).map((g) => g.body.events.length);
  assert.deepEqual(sizes, [100, 100, 50]);
  await done(s);
});

test("queue is bounded at 10,000", async () => {
  prophecy.init({ ingestKey: "lj_app_k", endpoint: "http://127.0.0.1:9", handlers: false, flushIntervalMs: 60000, retries: 0 });
  prophecy._state.draining = true; // park the sender so the queue only fills
  for (let i = 0; i < 10500; i++) prophecy.track("e");
  assert.equal(prophecy._state.queue.length, 10000);
  assert.equal(prophecy._state.dropped, 500);
  prophecy._state.queue.length = 0; prophecy._state.draining = false;
  await prophecy.close();
});

test("retries on 503 then succeeds; 401 disables quietly", async () => {
  let s = await setup([[503], [429, { "retry-after": "0" }]]);
  prophecy.track("x");
  assert.ok(await prophecy.flush());
  assert.deepEqual(s.hits, [503, 429, 200]);
  await done(s);
  s = await setup([[401]]);
  const orig = console.error; let logged = 0; console.error = () => { logged++; };
  prophecy.track("a"); await prophecy.flush(); prophecy.track("b"); await prophecy.flush();
  console.error = orig;
  assert.equal(logged, 1); assert.equal(s.hits.length, 1);
  await done(s);
});

test("tracking never throws into the host app, logs once", async () => {
  prophecy.init({ ingestKey: "lj_app_k", endpoint: "http://127.0.0.1:9", handlers: false, flushIntervalMs: 60000, retries: 0 });
  const orig = console.error; let logged = 0; console.error = () => { logged++; };
  const circular = {}; circular.self = circular;
  assert.doesNotThrow(() => { prophecy.track("x", { props: circular }); prophecy.track(undefined, null); prophecy.payment(); prophecy.signup(null); prophecy.identify(); prophecy.feature(); prophecy.subscription("u", undefined); prophecy.churn(); });
  await prophecy.flush(500);
  console.error = orig;
  assert.ok(logged <= 3);
  prophecy._state.queue.length = 0;
  await prophecy.close(100);
  prophecy.init({}); // no key: calls are no-ops
  assert.doesNotThrow(() => prophecy.track("x"));
});

test("a secret key is refused as ingest key; secret calls refuse in a browser", async () => {
  const orig = console.error; console.error = () => {};
  assert.equal(prophecy.init({ ingestKey: "ljp_sk_" + "b".repeat(32) }), false);
  console.error = orig;
  const s = await setup();
  globalThis.window = {};
  try {
    for (const f of [() => prophecy.report(), () => prophecy.refresh(), () => prophecy.live(), () => prophecy.embedUrl(60), () => prophecy.importHistory("events", [])]) {
      await assert.rejects(f, /never run in a browser/);
    }
    prophecy.init({ ingestKey: "lj_app_k", secretKey: "ljp_sk_" + "c".repeat(32), endpoint: s.url, handlers: false });
    assert.equal(prophecy._state.secretKey, null, "secretKey dropped in a browser");
  } finally { delete globalThis.window; }
  await done(s);
});

test("secret-key calls: report, refresh, live, embedUrl", async () => {
  const s = await setup();
  await prophecy.report("revenue"); await prophecy.refresh(); await prophecy.live();
  assert.equal(await prophecy.embedUrl(999999), "https://x/prophecy-embed/tok");
  const [r, f, l, e] = s.got;
  assert.equal(r.method, "GET"); assert.equal(r.path, "/api/v1/prophecy/sdk/report?section=revenue");
  assert.equal(r.headers.authorization, "Bearer ljp_sk_" + "a".repeat(32));
  assert.equal(f.method, "POST"); assert.equal(f.path, "/api/v1/prophecy/sdk/refresh");
  assert.equal(l.path, "/api/v1/prophecy/sdk/live");
  assert.equal(e.body.ttl, 86400);
  await done(s);
  await assert.rejects(async () => { prophecy.init({ ingestKey: "k" }); await prophecy.report(); }, /secretKey missing/);
});

test("middleware records the route template, never the raw path, query or bodies", async () => {
  const s = await setup();
  const mw = prophecy.middleware({ ignorePaths: ["/health"] });
  const run = (req) => { const res = new EventEmitter(); res.statusCode = req.status || 200; let called = 0; mw(req, res, () => called++); assert.equal(called, 1); res.emit("finish"); };
  run({ method: "GET", originalUrl: "/api/users/42?token=abc", baseUrl: "/api", route: { path: "/users/:id" }, headers: { authorization: "x" }, body: { password: "p" } });
  run({ method: "POST", originalUrl: "/orders/7f9c2c0e-1b2a-4c3d-8e9f-0a1b2c3d4e5f/items/12345?x=1", status: 404 });
  run({ method: "GET", originalUrl: "/health" });
  await prophecy.flush();
  const e = evs(s);
  assert.equal(e.length, 2);
  assert.equal(e[0].kind, "request");
  assert.equal(e[0].props.route, "/api/users/:id"); assert.equal(e[0].props.method, "GET"); assert.equal(e[0].props.status, 200);
  assert.equal(typeof e[0].props.duration_ms, "number");
  assert.equal(e[1].props.route, "/orders/:id/items/:id"); assert.equal(e[1].props.status, 404);
  const wire = JSON.stringify(s.got);
  for (const leak of ["token=abc", "password", "authorization", "12345", "42"]) assert.ok(!wire.includes(leak) || leak === "42" , leak);
  assert.ok(!wire.includes("token=abc") && !wire.includes("12345"));
  await done(s);
});

test("nextMiddleware wraps a route handler and rethrows", async () => {
  const s = await setup();
  const ok = prophecy.nextMiddleware(async () => ({ status: 201 }));
  await ok({ url: "http://h/api/items/99", method: "POST" });
  const bad = prophecy.nextMiddleware(async () => { throw new Error("boom"); }, { route: "/api/x" });
  await assert.rejects(bad({ url: "http://h/api/x", method: "GET" }), /boom/);
  await prophecy.flush();
  const e = evs(s);
  assert.equal(e[0].props.route, "/api/items/:id"); assert.equal(e[0].props.status, 201);
  assert.equal(e[1].props.route, "/api/x"); assert.equal(e[1].props.status, 500);
  await done(s);
});

test("importHistory batches 5,000 rows, hashes uids, reports progress, retries", async () => {
  const s = await setup([[503], [429, { "retry-after": "0" }]]);
  const rows = Array.from({ length: 12000 }, (_, i) => ({ ts: "2025-01-01T00:00:00Z", uid: "u" + i, name: "x", amount: String(i % 2 ? "9.5" : ""), id: "r" + i }));
  const prog = [];
  const r = await prophecy.importHistory("events", rows, { onProgress: (p) => prog.push(p.done) });
  assert.deepEqual(s.hits.slice(0, 3), [503, 429, 200]);
  assert.deepEqual(s.got.map((g) => g.body.rows.length), [5000, 5000, 2000]);
  assert.deepEqual(prog, [5000, 10000, 12000]);
  assert.equal(r.accepted, 12000);
  assert.equal(s.got[0].body.kind, "events"); assert.equal(s.got[0].body.rows[0].uid, sha("u0"));
  assert.strictEqual(s.got[0].body.rows[1].amount, 9.5); assert.ok(!("amount" in s.got[0].body.rows[0]));
  await assert.rejects(prophecy.importHistory("nope", []), /kind/);
  await done(s);
});

test("importHistory surfaces a 4xx without retrying", async () => {
  const s = await setup([[402]]);
  await assert.rejects(prophecy.importHistory("customers", [{ uid: "a" }]), /402/);
  assert.deepEqual(s.hits, [402]);
  await done(s);
});

test("csv parser handles quotes and header mapping", () => {
  const rows = parseCsv('ts,uid,name\r\n2025-01-01,"a,b","say ""hi"""\n2025-01-02,c,\n');
  assert.deepEqual(rows, [{ ts: "2025-01-01", uid: "a,b", name: 'say "hi"' }, { ts: "2025-01-02", uid: "c" }]);
});

// ------------------------------------------------------------------ scanner
const w = (root, rel, text) => { const p = path.join(root, rel); fs.mkdirSync(path.dirname(p), { recursive: true }); fs.writeFileSync(p, text); };
function fixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "prophecy-"));
  const AKIA = "AKIA" + "ABCDEFGHIJKLMNOP", SK = "sk_live_" + "x".repeat(24);
  w(root, "package.json", JSON.stringify({ name: "shop", dependencies: { express: "^4.18.0", next: "14.0.0", stripe: "^14.0.0", react: "18.2.0" }, devDependencies: { jest: "^29.0.0" } }));
  w(root, "package-lock.json", "{}");
  w(root, "README.md", "# Shop\n\nA **great** [shop](http://x.y) for shoes.\n\n```js\nconst k = '" + AKIA + "';\n```\n");
  w(root, "server.js", `const app = require("express")();\napp.get("/api/users/:id", h);\napp.post('/billing/charge', h);\nconst k = process.env.STRIPE_SECRET_KEY; const d = process.env["DATABASE_URL"];\nif (flags.betaSearch && isEnabled("new-checkout")) {}\nconst key = "${AKIA}"; const t = "${SK}";\n`);
  w(root, "app/api/orders/route.ts", "export async function GET() {}\nexport async function POST() {}\n");
  w(root, "app/(shop)/products/[slug]/page.tsx", "export default function P() {}\n");
  w(root, "pages/about.tsx", "export default function A() {}\n");
  w(root, "pages/api/ping.ts", "export default function h() {}\n");
  w(root, "billing/plans.js", "const plans = { pro: { price: 29, currency: 'usd' } };\n");
  w(root, "api/urls.py", "from django.urls import path\nurlpatterns = [path('invoices/<int:pk>/', v), path('health/', v)]\n");
  w(root, "api/app.py", "import os\n@app.route('/items', methods=['GET', 'POST'])\ndef a(): os.environ['PY_SECRET']\n@app.get('/fast/{id}')\ndef b(): pass\n");
  w(root, "routes/web.php", "<?php\nRoute::get('/dashboard', fn() => 1);\n");
  w(root, "config/routes.rb", "get '/ping', to: 'x#y'\nresources :widgets\n");
  w(root, "main.go", 'package main\nfunc main(){ http.HandleFunc("/healthz", h); r.Get("/v1/things", h) }\n');
  w(root, "requirements.txt", "flask==2.3.1\nrequests>=2.0\n# c\n");
  w(root, "go.mod", "module x\nrequire (\n\tgithub.com/gin-gonic/gin v1.9.1\n)\n");
  w(root, "composer.json", JSON.stringify({ require: { php: ">=8", "laravel/framework": "^10.0" } }));
  w(root, "Gemfile", "gem 'rails', '~> 7.0'\n");
  w(root, ".env", "SECRET_VALUE_SHOULD_NOT_BE_READ=hunter2\n");
  w(root, ".env.example", "PORT=3000\nSTRIPE_SECRET_KEY=sk_test_placeholder\n# comment\nexport MAIL_HOST=smtp\n");
  w(root, "Dockerfile", "FROM node\n");
  w(root, "docker-compose.yml", "services: {}\n");
  w(root, "k8s/deploy.yaml", "apiVersion: apps/v1\nkind: Deployment\n");
  w(root, "infra/main.tf", "resource {}\n");
  w(root, ".github/workflows/ci.yml", "on: push\n");
  w(root, "vercel.json", "{}");
  w(root, "tests/a.test.js", "const test = require('node:test');\n");
  w(root, "node_modules/evil/index.js", 'app.get("/should/not/appear", h);\nconst k = "' + AKIA + '";\n');
  w(root, "dist/out.js", 'app.get("/dist/no", h);\n');
  w(root, "big.js", "x".repeat(1024 * 1024 + 10));
  return { root, AKIA, SK };
}

test("scanner builds the manifest without leaking secrets or env values", () => {
  const { root, AKIA, SK } = fixture();
  const m = prophecy.scan(root);
  assert.equal(m.schema, 1); assert.equal(m.root_name, path.basename(root));
  const routes = m.routes.map((r) => r.method + " " + r.path);
  for (const want of ["GET /api/users/:id", "POST /billing/charge", "GET /api/orders", "POST /api/orders", "GET /products/:slug", "GET /about", "GET /api/ping",
    "ANY invoices/<int:pk>/".replace("invoices/<int:pk>/", "/invoices/:pk/"), "GET /items", "POST /items", "GET /fast/{id}", "GET /dashboard", "GET /ping", "GET /widgets", "DELETE /widgets/:id", "ANY /healthz", "GET /v1/things"]) {
    assert.ok(routes.includes(want), "missing route " + want + " in " + routes.join(" | "));
  }
  assert.ok(!routes.some((r) => /should\/not|dist\/no/.test(r)), "skips node_modules and dist");
  assert.ok(!Object.keys(m.languages).includes("md"));
  assert.ok(m.languages.js >= 3 && m.languages.py === 2 && m.loc > 20);
  assert.ok(m.frameworks.includes("express") && m.frameworks.includes("next") && m.frameworks.includes("laravel") && m.frameworks.includes("gin"));
  const dep = (e, n) => m.dependencies.find((d) => d.ecosystem === e && d.name === n);
  assert.equal(dep("npm", "express").version, "^4.18.0"); assert.equal(dep("pypi", "flask").version, "2.3.1");
  assert.equal(dep("go", "github.com/gin-gonic/gin").version, "v1.9.1"); assert.ok(dep("composer", "laravel/framework")); assert.ok(!dep("composer", "php")); assert.ok(dep("gem", "rails"));
  assert.ok(m.integrations.includes("stripe"));
  for (const n of ["PORT", "STRIPE_SECRET_KEY", "MAIL_HOST", "DATABASE_URL", "PY_SECRET"]) assert.ok(m.env_names.includes(n), n);
  assert.ok(!m.env_names.includes("SECRET_VALUE_SHOULD_NOT_BE_READ"), ".env is never read");
  assert.ok(m.features.some((f) => f.source === "flag" && f.name === "new-checkout") && m.features.some((f) => f.source === "flag" && f.name === "betaSearch"));
  assert.ok(m.features.some((f) => f.source === "route" && f.name === "billing") && m.features.some((f) => f.source === "dir"));
  assert.equal(m.infra.docker, true); assert.equal(m.infra.compose, true); assert.equal(m.infra.kubernetes, true); assert.equal(m.infra.terraform, true);
  assert.deepEqual(m.infra.ci, ["github-actions"]); assert.deepEqual(m.infra.hosting_hints, ["vercel"]);
  assert.equal(m.security.lockfile, true); assert.equal(m.security.env_committed, true);
  assert.equal(m.security.secrets_suspected, 3, "2 in server.js + 1 in README; node_modules skipped; counts only");
  assert.ok(m.pricing_hints.some((p) => p.plan === "pro" && p.amount === 29 && p.currency === "USD"));
  assert.ok(m.description.startsWith("Shop A great shop for shoes.") && !m.description.includes("AKIA"));
  assert.equal(m.tests.files, 1); assert.ok(m.tests.frameworks.includes("jest"));
  const wire = JSON.stringify(m);
  for (const leak of [AKIA, SK, "hunter2", "sk_test_placeholder", "app.get("]) assert.ok(!wire.includes(leak), "leaked " + leak);
  fs.rmSync(root, { recursive: true });
});

test("env_committed is false when .env is gitignored; symlinks out of the root are not followed", () => {
  const { root } = fixture();
  w(root, ".gitignore", "node_modules\n.env\n");
  const outside = fs.mkdtempSync(path.join(os.tmpdir(), "outside-"));
  w(outside, "leak.js", 'app.get("/outside/route", h);\n');
  try { fs.symlinkSync(outside, path.join(root, "linked")); fs.symlinkSync(path.join(outside, "leak.js"), path.join(root, "leak.js")); } catch (_) { /* no symlinks */ }
  const m = prophecy.scan(root);
  assert.equal(m.security.env_committed, false);
  assert.ok(!m.routes.some((r) => r.path === "/outside/route"));
  fs.rmSync(root, { recursive: true }); fs.rmSync(outside, { recursive: true });
});

test("scanner respects the file cap and is fast", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "prophecy-big-"));
  for (let d = 0; d < 20; d++) for (let f = 0; f < 60; f++) w(root, `d${d}/f${f}.js`, `app.get("/r${d}/${f}", h);\n`);
  const t0 = Date.now();
  const m = prophecy.scan(root, { maxFiles: 500 });
  assert.ok(Date.now() - t0 < 5000);
  assert.ok(m.routes.length <= 500 && m.routes.length > 100, String(m.routes.length));
  const full = prophecy.scan(root);
  assert.equal(full.routes.length, 1200);
  fs.rmSync(root, { recursive: true });
});

test("sendManifest posts to /ingest/v1/prophecy/manifest and trims oversize manifests", async () => {
  const s = await setup();
  const m = prophecy.scan(__dirname + "/..");
  m.routes = Array.from({ length: 6000 }, (_, i) => ({ method: "GET", path: "/some/long/route/number/" + i + "/" + "x".repeat(60), file: "f".repeat(60) }));
  assert.equal(await prophecy.sendManifest(m), true);
  const g = s.got[0];
  assert.equal(g.path, "/ingest/v1/prophecy/manifest"); assert.ok(g.gz); assert.equal(g.body.manifest.schema, 1);
  assert.ok(JSON.stringify(g.body).length <= 512 * 1024); assert.ok(g.body.manifest.routes.length < 6000);
  await done(s);
});
