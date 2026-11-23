const test = require("node:test");
const assert = require("node:assert");
const http = require("node:http");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawn } = require("node:child_process");
const { Client, ApiError, gateFindings } = require("../products.cjs");
const lj = require("../index.cjs");
const cli = require("../bin/lj.cjs");

const KEY = "ljk_" + "test1234_" + "x".repeat(32);
const BIN = path.join(__dirname, "..", "bin", "lj.cjs");

/** Fake product API. `routes` maps "METHOD /api/v1/path" to a body, a status/body pair or a function(req, body, calls). */
function fake(routes) {
  const calls = [];
  const srv = http.createServer((req, res) => {
    let b = ""; req.on("data", (c) => (b += c)); req.on("end", () => {
      const u = new URL(req.url, "http://x");
      const rec = { method: req.method, path: u.pathname, query: Object.fromEntries(u.searchParams), auth: req.headers.authorization, ua: req.headers["user-agent"], body: b ? JSON.parse(b) : undefined };
      calls.push(rec);
      let r = routes[`${req.method} ${u.pathname}`];
      if (typeof r === "function") r = r(rec, calls);
      if (r === undefined) r = [404, { error: { code: "not_found", message: "Not found." } }];
      const [code, body, headers] = Array.isArray(r) && typeof r[0] === "number" ? r : [200, r];
      res.writeHead(code, { "Content-Type": typeof body === "string" ? "text/x-diff" : "application/json", ...(headers || {}) });
      res.end(typeof body === "string" ? body : JSON.stringify(body));
    });
  });
  return new Promise((r) => srv.listen(0, "127.0.0.1", () => r({ srv, calls, url: `http://127.0.0.1:${srv.address().port}` })));
}
const A = "/api/v1/";
function runCli(args, env, input) {
  return new Promise((resolve) => {
    const p = spawn(process.execPath, [BIN, ...args], { env: { PATH: process.env.PATH, HOME: os.tmpdir(), ...env } });
    let out = "", err = "";
    p.stdout.on("data", (d) => (out += d)); p.stderr.on("data", (d) => (err += d));
    p.on("close", (code) => resolve({ code, out, err }));
    if (input !== undefined) p.stdin.end(input); else p.stdin.end();
  });
}
const FIND = (sev) => ({ id: 1, severity: sev, category: "xss", title: "Issue " + sev });

test("client sends bearer auth, user agent and JSON, and builds typed errors", async () => {
  const f = await fake({
    [`GET ${A}apikeys/whoami`]: { organization: { id: 1, name: "Acme" }, scopes: ["fix"], key: { prefix: "test1234" } },
    [`POST ${A}fixops/jobs/5/reject`]: (rq) => [402, { error: { code: "not_subscribed", message: "Subscribe first." } }],
  });
  try {
    const c = new Client({ apiKey: KEY, baseUrl: f.url + "/", org: 9 });
    const w = await c.whoami();
    assert.equal(w.organization.name, "Acme");
    assert.equal(f.calls[0].auth, "Bearer " + KEY);
    assert.match(f.calls[0].ua, /^linearjeph-node\/1\.3\.0$/);
    assert.equal(f.calls[0].query.org, "9");
    await assert.rejects(c.fix.jobs.reject(5, "no"), (e) => e instanceof ApiError && e.status === 402 && e.code === "not_subscribed" && e.message === "Subscribe first.");
    assert.deepEqual(f.calls[1].body, { reason: "no" });
    assert.ok(!JSON.stringify(c).includes(KEY), "key never serialised");
    assert.throws(() => c.fix.jobs.get("abc"), /whole number/);
    await assert.rejects(new Client({ apiKey: "" , baseUrl: f.url}).whoami(), (e) => e.code === "no_key");
    assert.equal(typeof lj.Client, "function");
  } finally { f.srv.close(); }
});

test("GET retries 429 and 5xx with Retry-After, POST does not retry", async () => {
  let n = 0, m = 0;
  const f = await fake({
    [`GET ${A}fixops/overview`]: () => (++n < 3 ? [n === 1 ? 429 : 503, { error: { code: "busy", message: "busy" } }, n === 1 ? { "Retry-After": "0" } : {}] : { ok: true }),
    [`POST ${A}fixops/jobs/1/cancel`]: () => { m++; return [503, { error: { code: "down", message: "down" } }]; },
  });
  try {
    const c = new Client({ apiKey: KEY, baseUrl: f.url, backoffMs: 1 });
    assert.deepEqual(await c.fix.overview(), { ok: true });
    assert.equal(n, 3);
    await assert.rejects(c.fix.jobs.cancel(1), (e) => e.status === 503);
    assert.equal(m, 1);
  } finally { f.srv.close(); }
});

test("namespaces send the documented methods, paths and bodies", async () => {
  const f = await fake({});
  const c = new Client({ apiKey: KEY, baseUrl: f.url });
  const ok = (r) => r;
  const routes = {};
  for (const m of ["GET", "POST", "PATCH"]) void m;
  // every call 404s; we only look at what was sent
  const tries = [
    () => c.fix.jobs.list({ project: 3, status: "failed" }), () => c.fix.jobs.create({ source_id: "s", title: "t", issue: "i" }), () => c.fix.jobs.events(4, 10),
    () => c.fix.jobs.accept(4), () => c.fix.jobs.rollback(4), () => c.fix.jobs.patch(4), () => c.fix.projects.update(2, { deps_enabled: true }),
    () => c.fix.projects.review(2), () => c.fix.projects.deps(2), () => c.fix.projects.tests(2, 3), () => c.fix.migrations.plan(8),
    () => c.shield.vibe.scan(12), () => c.shield.vibe.scanDetail(7), () => c.shield.secrets.findings({ status: "open" }), () => c.shield.secrets.scan(2),
    () => c.shield.pentest.scan(4), () => c.shield.pentest.retest(9), () => c.shield.pentest.history(4), () => c.shield.waf.events(6), () => c.shield.security.start(5),
    () => c.ops.incidents.resolve(3), () => c.ops.cost.analyse(), () => c.ops.managedDatabases.list(), () => c.ops.backups.run(2), () => c.ops.backups.drill(2), () => c.ops.backups.runs(2),
    () => c.models.endpoints.action(1, "suspend"), () => c.models.private.logs(1), () => c.models.finetune.jobAction(3, "cancel"),
    () => c.agents.run(5, { a: 1 }), () => c.agents.action(5, "pause"), () => c.agents.runDetail(5, 6), () => c.agents.cancelRun(5, 6),
    () => c.comply.recheck(), () => c.comply.tasks.update(3, { status: "done" }), () => c.comply.export(),
  ];
  try {
    for (const t of tries) await t().catch(() => {});
    const seen = f.calls.map((x) => `${x.method} ${x.path.replace(A, "")}${Object.keys(x.query).length ? "?" + new URLSearchParams(x.query) : ""}`);
    assert.deepEqual(seen, [
      "GET fixops/jobs?project=3&status=failed", "POST fixops/jobs", "GET fixops/jobs/4/events?after=10", "POST fixops/jobs/4/accept", "POST fixops/jobs/4/rollback",
      "GET fixops/jobs/4/patch", "PATCH fixops/projects/2", "POST fixops/projects/2/review-now", "POST fixops/projects/2/deps-now", "POST fixops/projects/2/tests-now",
      "POST fixops/migrations/8/plan", "POST shield/vibe/projects/12/scan", "GET shield/vibe/scans/7", "GET shield/secrets/findings?status=open", "POST shield/secrets/sources/2/scan",
      "POST shield/pentest/targets/4/scan", "POST shield/pentest/findings/9/retest", "GET shield/pentest/targets/4/history", "GET shield/waf/domains/6/events", "POST security/scans",
      "POST opsagent/incidents/3/resolve", "POST opsagent/cost/analyse", "GET opsagent/managed-db", "POST backups/agent/targets/2/run", "POST backups/agent/targets/2/drill", "GET backups/agent/targets/2/runs",
      "POST endpoints/endpoints/1/suspend", "GET agentcloud/private/endpoints/1/logs", "POST agentcloud/finetune/jobs/3/cancel",
      "POST agentcloud/agents/5/run", "POST agentcloud/agents/5/pause", "GET agentcloud/agents/5/runs/6", "POST agentcloud/agents/5/runs/6",
      "POST comply/compliance/recheck", "PATCH comply/compliance/tasks/3", "GET comply/compliance/export",
    ]);
    assert.deepEqual(f.calls[1].body, { source_id: "s", title: "t", issue: "i" });
    assert.deepEqual(f.calls[9].body, { max_files: 3 });
    assert.deepEqual(f.calls[19].body, { target: 5 });
    assert.deepEqual(f.calls[29].body, { input: { a: 1 } });
  } finally { f.srv.close(); }
});

test("fix.run resolves a project id to its source, fix.wait stops at final or pr_open", async () => {
  let polls = 0;
  const f = await fake({
    [`GET ${A}fixops/projects/12`]: { id: 12, source_id: "github:acme/app" },
    [`POST ${A}fixops/jobs`]: (rq) => [201, { id: 40, status: "queued", title: rq.body.title, project: 12 }],
    [`GET ${A}fixops/jobs/40`]: () => ({ id: 40, status: ++polls < 3 ? "patching" : "pr_open", pr_url: "https://example.test/pr/1" }),
  });
  try {
    const c = new Client({ apiKey: KEY, baseUrl: f.url });
    const j = await c.fix.run(12, { issue: "Cart crashes\nmore detail" });
    assert.equal(j.title, "Cart crashes");
    assert.equal(f.calls.find((x) => x.method === "POST").body.source_id, "github:acme/app");
    const seen = [];
    const d = await c.fix.wait(40, { interval: 0.01, onUpdate: (x) => seen.push(x.status) });
    assert.equal(d.status, "pr_open"); assert.deepEqual(seen, ["patching", "patching", "pr_open"]);
    await assert.rejects(c._poll(async () => ({ status: "x" }), () => false, { timeout: 0.05, interval: 0.2 }), (e) => e.code === "timeout");
  } finally { f.srv.close(); }
});

test("gate returns findings at or above the severity", () => {
  const scan = { findings: ["info", "low", "medium", "high", "critical"].map(FIND) };
  assert.deepEqual(gateFindings(scan, "high").map((f) => f.severity), ["high", "critical"]);
  assert.equal(gateFindings(scan, "low").length, 4);
  assert.equal(gateFindings({ findings: [] }, "critical").length, 0);
  assert.throws(() => gateFindings(scan, "urgent"), ApiError);
});

test("binary and text responses are not parsed as JSON", async () => {
  const f = await fake({ [`GET ${A}comply/compliance/export`]: () => [200, "PK\u0003\u0004", {}] });
  try {
    const c = new Client({ apiKey: KEY, baseUrl: f.url });
    assert.equal(typeof (await c.comply.export()), "string");
  } finally { f.srv.close(); }
});

function cliEnv(url, extra = {}) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "lj-test-"));
  return { env: { LJ_API_KEY: KEY, LJ_BASE_URL: url, XDG_CONFIG_HOME: home, ...extra }, home };
}

test("lj shield gate: exit 0 clean, exit 1 with findings, --json shape", async () => {
  let findings = [FIND("low")];
  const f = await fake({
    [`POST ${A}shield/vibe/projects/12/scan`]: [201, { id: 70, status: "queued" }],
    [`GET ${A}shield/vibe/scans/70`]: () => ({ id: 70, status: "done", counts: { low: findings.length }, grade: "B", score: 80, findings }),
  });
  const { env } = cliEnv(f.url);
  try {
    let r = await runCli(["shield", "gate", "--project", "12", "--fail-on", "high", "--interval", "0.01"], env);
    assert.equal(r.code, 0, r.err + r.out); assert.match(r.out, /PASS/);
    findings = [FIND("low"), FIND("critical")];
    r = await runCli(["shield", "gate", "--project", "12", "--fail-on", "high", "--json", "--interval", "0.01"], env);
    assert.equal(r.code, 1);
    const j = JSON.parse(r.out);
    assert.equal(j.passed, false); assert.equal(j.fail_on, "high"); assert.equal(j.scan.id, 70); assert.equal(j.findings.length, 1);
    r = await runCli(["linearjeph", "shield"].slice(1).concat(["gate", "--project", "12", "--fail-on", "nope"]), env);
    assert.equal(r.code, 2);
  } finally { f.srv.close(); }
});

test("lj fix run --wait: exit 0 when a PR opens, 1 when the job fails; linearjeph <product> works", async () => {
  let final = "pr_open", n = 0;
  const f = await fake({
    [`GET ${A}fixops/projects/12`]: { id: 12, source_id: "github:acme/app" },
    [`POST ${A}fixops/jobs`]: [201, { id: 41, status: "queued", title: "x" }],
    [`GET ${A}fixops/jobs/41`]: () => ({ id: 41, status: ++n % 2 ? "testing" : final, title: "x", pr_url: final === "pr_open" ? "https://example.test/pr/2" : "", error: final === "failed" ? "tests still fail" : "" }),
  });
  const { env } = cliEnv(f.url);
  try {
    let r = await runCli(["fix", "run", "--project", "12", "--issue", "Boom happens", "--wait", "--interval", "0.01"], env);
    assert.equal(r.code, 0, r.err); assert.match(r.out, /pr_open/); assert.match(r.err, /testing/);
    final = "failed"; n = 0;
    r = await runCli(["fix", "run", "--project", "12", "--issue", "Boom happens", "--wait", "--interval", "0.01"], env);
    assert.equal(r.code, 1); assert.match(r.out, /tests still fail/);
    const post = f.calls.filter((c) => c.method === "POST")[0];
    assert.deepEqual(post.body, { source_id: "github:acme/app", title: "Boom happens", issue: "Boom happens" });
    // via the linearjeph binary
    const p = await new Promise((res) => { const c = spawn(process.execPath, [path.join(__dirname, "..", "bin", "linearjeph.cjs"), "fix", "status", "41", "--json"], { env: { ...env, PATH: process.env.PATH } }); let o = ""; c.stdout.on("data", (d) => (o += d)); c.on("close", (code) => res({ code, o })); });
    assert.equal(p.code, 0); assert.equal(JSON.parse(p.o).id, 41);
  } finally { f.srv.close(); }
});

test("lj login stores the key 0600 without printing it; whoami and logout", async () => {
  const f = await fake({ [`GET ${A}apikeys/whoami`]: { organization: { id: 1, name: "Acme" }, scopes: ["fix", "shield"], key: { prefix: "test1234", name: "ci" } } });
  const { env, home } = cliEnv(f.url, { LJ_API_KEY: "" });
  delete env.LJ_API_KEY;
  try {
    let r = await runCli(["login"], env, KEY + "\n");
    assert.equal(r.code, 0, r.err);
    assert.ok(!r.out.includes(KEY) && !r.err.includes(KEY));
    const file = path.join(home, "linearjeph", "credentials");
    assert.equal(fs.statSync(file).mode & 0o777, 0o600);
    assert.equal(fs.statSync(path.dirname(file)).mode & 0o777, 0o700);
    assert.equal(JSON.parse(fs.readFileSync(file, "utf8")).api_key, KEY);
    r = await runCli(["whoami"], env);
    assert.equal(r.code, 0); assert.match(r.out, /Acme/); assert.ok(!r.out.includes(KEY));
    r = await runCli(["logout"], env); assert.equal(r.code, 0); assert.ok(!fs.existsSync(file));
    r = await runCli(["whoami"], env); assert.equal(r.code, 2); assert.match(r.err, /no API key/);
    r = await runCli(["login", "--key", "nonsense"], env); assert.equal(r.code, 2);
  } finally { f.srv.close(); }
});

test("lj api, auth errors, usage errors and help", async () => {
  const f = await fake({
    [`GET ${A}fixops/jobs`]: { results: [{ id: 1, status: "done", kind: "fix", project: 2, title: "t", pr_url: "" }] },
    [`POST ${A}x/y`]: (rq) => ({ echo: rq.body }),
    [`GET ${A}opsagent/summary`]: [401, { error: { code: "unauthorized", message: "Invalid API key." } }],
  });
  const { env } = cliEnv(f.url);
  try {
    let r = await runCli(["api", "POST", "x/y", "--data", '{"a":1}'], env);
    assert.equal(r.code, 0); assert.deepEqual(JSON.parse(r.out), { echo: { a: 1 } });
    r = await runCli(["fix", "jobs"], env); assert.match(r.out, /ID\s+STATUS/); assert.match(r.out, /done/);
    r = await runCli(["ops", "status"], env); assert.equal(r.code, 2); assert.match(r.err, /Invalid API key/); assert.ok(!r.err.includes(KEY));
    r = await runCli(["fix", "nonsense"], env); assert.equal(r.code, 2);
    r = await runCli(["fix", "run", "--project", "1"], env); assert.equal(r.code, 2);
    r = await runCli(["--help"], {}); assert.equal(r.code, 0); assert.match(r.out, /lj shield --help|lj <product> --help/);
    for (const p of ["fix", "shield", "ops", "models", "agents", "comply"]) { r = await runCli([p, "--help"], {}); assert.equal(r.code, 0); assert.match(r.out, new RegExp("lj " + p)); }
  } finally { f.srv.close(); }
});

test("cli table output and credential reader accept bare key lines", () => {
  assert.match(cli.table([{ a: 1, b: "x" }], [["A", "a"], ["B", "b"]]), /A\s+B\n1\s+x/);
  const { env, home } = cliEnv("http://x");
  const file = cli.credentialsPath(env);
  fs.mkdirSync(path.dirname(file), { recursive: true }); fs.writeFileSync(file, KEY + "\n");
  assert.equal(cli.readCredentials(env).api_key, KEY);
});
