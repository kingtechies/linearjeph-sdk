const test = require("node:test");
const assert = require("node:assert");
const http = require("node:http");
const zlib = require("node:zlib");
const lj = require("../index.cjs");

function server(script = []) {
  const got = []; const queue = [...script];
  const srv = http.createServer((req, res) => {
    const chunks = []; req.on("data", (c) => chunks.push(c)); req.on("end", () => {
      let raw = Buffer.concat(chunks);
      const enc = req.headers["content-encoding"];
      if (enc === "gzip") raw = zlib.gunzipSync(raw);
      if (queue.length) { const [code, headers] = queue.shift(); res.writeHead(code, headers || {}); return res.end(); }
      got.push({ path: req.url, key: req.headers["x-lj-key"], enc, body: JSON.parse(raw) });
      res.statusCode = 202; res.end('{"accepted":1}');
    });
  });
  return new Promise((r) => srv.listen(0, "127.0.0.1", () => r({ srv, got, url: `http://127.0.0.1:${srv.address().port}` })));
}
const setup = async (extra = {}, script) => {
  const s = await server(script);
  lj.init({ key: "lj_app_k", endpoint: s.url, release: "2.0.0", handlers: false, backoffMs: 5, ...extra });
  return s;
};
const items = (s, suffix, key) => s.got.filter((g) => g.path.endsWith(suffix)).flatMap((g) => g.body[key]);
const done = (s) => { s.srv.closeAllConnections(); s.srv.close(); };

test("metrics aggregate per series and flush", async () => {
  const s = await setup();
  for (let i = 0; i < 5; i++) lj.incr("jobs.done", 1, { queue: "mail" });
  lj.incr("jobs.done", 10, { queue: "mail" });
  lj.gauge("queue.depth", 3); lj.gauge("queue.depth", 9);
  for (const v of [10, 20, 30]) lj.histogram("lat", v);
  assert.equal(await lj.time("work", async () => { await new Promise((r) => setTimeout(r, 15)); return 42; }, { kind: "x" }), 42);
  assert.ok(await lj.flush());
  const m = Object.fromEntries(items(s, "/metrics", "metrics").map((x) => [x.name, x]));
  assert.equal(m["jobs.done"].type, "counter"); assert.equal(m["jobs.done"].value, 15); assert.deepEqual(m["jobs.done"].tags, { queue: "mail" });
  assert.equal(m["queue.depth"].value, 9);
  assert.deepEqual([m.lat.count, m.lat.sum, m.lat.min, m.lat.max], [3, 60, 10, 30]); assert.deepEqual(m.lat.values.sort(), [10, 20, 30]);
  assert.ok(m.work.min >= 10 && m.work.tags.kind === "x");
  done(s);
});

test("metrics are sent on a timer without flush()", async () => {
  const s = await setup({ metricsIntervalMs: 1000 });
  lj.incr("tick");
  const end = Date.now() + 4000;
  while (Date.now() < end && !items(s, "/metrics", "metrics").length) await new Promise((r) => setTimeout(r, 100));
  assert.equal(items(s, "/metrics", "metrics")[0].name, "tick");
  done(s);
});

test("metric validation, series cap and tag scrubbing", async () => {
  const s = await setup({ maxMetricSeries: 3 });
  lj.incr("bad name!"); lj.incr("ok", NaN); lj.incr("ok", "x");
  for (let i = 0; i < 6; i++) lj.incr("many", 1, { i: String(i) });
  lj.incr("p", 1, { password: "hunter2", email: "a@b.co" });
  assert.ok(lj.droppedMetricSeries() >= 3);
  await lj.flush();
  const ms = items(s, "/metrics", "metrics");
  assert.equal(ms.length, 3); assert.ok(!ms.some((m) => m.name === "bad name!" || m.name === "ok"));
  done(s);
});

test("large metric batches are gzip compressed", async () => {
  const s = await setup();
  for (let i = 0; i < 300; i++) lj.incr("series." + i, 1, { pad: "x".repeat(50) });
  await lj.flush();
  assert.ok(s.got.some((g) => g.path.endsWith("/metrics") && g.enc === "gzip"));
  assert.equal(items(s, "/metrics", "metrics").length, 300);
  done(s);
});

test("telemetry never throws before init", () => {
  lj.init({ key: "" });
  assert.doesNotThrow(() => { lj.incr("a"); lj.gauge("a", 1); lj.histogram("a", 1); lj.log("info", "x"); lj.checkin("job"); lj.release({}); lj.deploy({}); lj.startTimer("t").end(); lj.logs.info("x"); });
});

test("scrubPii removes emails, valid card numbers and tokens", () => {
  assert.equal(lj.scrubPii("mail bob@example.com"), "mail [email]");
  assert.equal(lj.scrubPii("card 4111 1111 1111 1111 ok"), "card [card] ok");
  assert.equal(lj.scrubPii("order 1234567890123 id"), "order 1234567890123 id");
  assert.match(lj.scrubPii("jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig"), /\[token\]/);
  assert.equal(lj.scrubPii("key lj_app_abcdefghijkl and sk-abcdefghijklmnopqrstuv"), "key [token] and [token]");
  assert.ok(!lj.scrubPii("password=hunter2 Bearer abcdefghij1234").includes("hunter2"));
});

test("logs: structured, scrubbed, level filtered, hook", async () => {
  const s = await setup({ logLevel: "info", beforeSendLog: (it) => (it.message.includes("drop") ? null : { ...it, context: { ...it.context, hooked: true } }) });
  lj.log("debug", "debug line");
  lj.log("error", "user ann@example.com paid 4242424242424242", { order: 5, password: "x", note: "a@b.co" }, "pay");
  lj.logs.warning("drop me");
  await lj.flush();
  const logs = items(s, "/logs", "logs");
  assert.equal(logs.length, 1);
  assert.equal(logs[0].level, "error"); assert.equal(logs[0].message, "user [email] paid [card]"); assert.equal(logs[0].logger, "pay");
  assert.deepEqual(logs[0].context, { order: 5, password: "[Filtered]", note: "[email]", hooked: true });
  done(s);
});

test("pino stream", async () => {
  const s = await setup();
  const pino = lj.pinoStream();
  pino.write(JSON.stringify({ level: 50, time: 1, pid: 1, hostname: "h", msg: "db down for a@b.co", name: "api", requestId: "r1" }) + "\n");
  pino.write("not json\n");
  await lj.flush();
  const logs = items(s, "/logs", "logs");
  assert.equal(logs[0].level, "error"); assert.equal(logs[0].message, "db down for [email]"); assert.equal(logs[0].logger, "api"); assert.equal(logs[0].context.requestId, "r1");
  done(s);
});

test("console capture forwards and still prints", async () => {
  const s = await setup();
  const lines = []; const orig = console.warn; console.warn = (...a) => lines.push(a.join(" "));
  const undo = lj.captureConsole(["warn"]);
  console.warn("careful", { a: 1 });
  undo(); console.warn = orig;
  await lj.flush();
  assert.equal(lines.length, 1); assert.match(lines[0], /^careful/);
  const l = items(s, "/logs", "logs")[0];
  assert.equal(l.level, "warning"); assert.equal(l.logger, "console"); assert.match(l.message, /careful/);
  done(s);
});

test("winston transport forwards log info", async () => {
  const s = await setup();
  const Module = require("node:module");
  const orig = Module._load;
  Module._load = function (req, ...a) { return req === "winston-transport" ? class { constructor(o) { this.o = o; } } : orig.call(this, req, ...a); };
  let T; try { T = lj.winstonTransport(); } finally { Module._load = orig; }
  const t = new T();
  let called = false;
  t.log({ level: "warn", message: "slow", route: "/x" }, () => { called = true; });
  await lj.flush();
  assert.ok(called);
  const l = items(s, "/logs", "logs")[0];
  assert.equal(l.level, "warning"); assert.equal(l.context.route, "/x"); assert.equal(l.logger, "winston");
  done(s);
});

test("spans nest, carry trace ids and join logs", async () => {
  const s = await setup();
  await lj.span("handle", async (root) => {
    assert.equal(lj.currentSpan(), root);
    await lj.span("db.query", async () => {}, { kind: "client" });
    await assert.rejects(lj.span("boom", async () => { throw new Error("k"); }), /k/);
    lj.log("error", "inside");
  }, { kind: "server", tags: { route: "/x" } });
  assert.equal(lj.currentSpan(), null);
  await lj.flush();
  const sp = Object.fromEntries(items(s, "/spans", "spans").map((x) => [x.name, x]));
  assert.deepEqual(Object.keys(sp).sort(), ["boom", "db.query", "handle"]);
  assert.equal(new Set(Object.values(sp).map((x) => x.trace_id)).size, 1);
  assert.equal(sp.handle.parent_id, ""); assert.equal(sp["db.query"].parent_id, sp.handle.span_id); assert.equal(sp.handle.span_id.length, 16);
  assert.equal(sp.boom.status, "error"); assert.equal(sp["db.query"].kind, "client"); assert.deepEqual(sp.handle.tags, { route: "/x" });
  const lg = items(s, "/logs", "logs")[0];
  assert.equal(lg.trace_id, sp.handle.trace_id); assert.equal(lg.span_id, sp.handle.span_id);
  done(s);
});

test("sampling and traceparent propagation", async () => {
  let s = await setup({ tracesSampleRate: 0 });
  await lj.span("dropped", async () => {}); await lj.flush(); assert.equal(items(s, "/spans", "spans").length, 0); done(s);
  s = await setup({ tracesSampleRate: 1 });
  const hdr = await lj.span("deco", async () => lj.injectHeaders({}));
  assert.match(hdr.traceparent, /^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
  const tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01";
  await lj.span("continued", async () => {}, { kind: "server", traceparent: tp });
  await lj.span("not-sampled", async () => {}, { traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00" });
  await lj.flush();
  const sp = Object.fromEntries(items(s, "/spans", "spans").map((x) => [x.name, x]));
  assert.deepEqual(Object.keys(sp).sort(), ["continued", "deco"]);
  assert.equal(sp.continued.trace_id, "0af7651916cd43dd8448eb211c80319c"); assert.equal(sp.continued.parent_id, "b7ad6b7169203331");
  assert.equal(lj.parseTraceparent("garbage"), null);
  done(s);
});

test("express middleware creates a server span and a transaction", async () => {
  const s = await setup();
  const { EventEmitter } = require("node:events");
  const res = new EventEmitter(); res.statusCode = 500;
  const req = { method: "GET", originalUrl: "/items/5?x=1", baseUrl: "", route: { path: "/items/:id" }, headers: { traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" } };
  let inside = null;
  lj.requestHandler()(req, res, () => { inside = lj.currentSpan(); });
  assert.ok(inside); res.emit("finish");
  await lj.flush();
  const sp = items(s, "/spans", "spans")[0];
  assert.equal(sp.name, "GET /items/:id"); assert.equal(sp.kind, "server"); assert.equal(sp.status, "error"); assert.equal(sp.trace_id, "0af7651916cd43dd8448eb211c80319c");
  const tx = items(s, "/events", "events")[0];
  assert.equal(tx.transaction.name, "GET /items/:id");
  done(s);
});

test("wrapNext covers Pages API routes and App Router handlers", async () => {
  const s = await setup();
  const res = { statusCode: 200, setHeader() {} };
  const out = await lj.wrapNext(async (req, r) => { r.statusCode = 201; return "x"; }, { name: "POST /api/items" })({ method: "POST", url: "/api/items", headers: {} }, res);
  assert.equal(out, "x");
  const r2 = await lj.wrapNext(async () => ({ status: 200 }), { name: "GET /api/ok" })({ method: "GET", url: "http://x/api/ok", headers: new Headers() });
  assert.equal(r2.status, 200);
  await assert.rejects(lj.wrapNext(async () => { throw new Error("route failed"); }, { name: "GET /api/bad" })({ method: "GET", url: "http://x/api/bad", headers: new Headers() }), /route failed/);
  await lj.flush();
  const tx = Object.fromEntries(items(s, "/events", "events").filter((e) => e.type === "transaction").map((e) => [e.transaction.name, e.transaction]));
  assert.equal(tx["POST /api/items"].status, "ok"); assert.equal(tx["GET /api/bad"].status, "error");
  assert.ok(items(s, "/events", "events").some((e) => e.type === "error" && e.exception.value === "route failed"));
  assert.equal(items(s, "/spans", "spans").length, 3);
  done(s);
});

test("monitor sends start then ok/fail with duration and schedule", async () => {
  const s = await setup();
  assert.equal(await lj.monitor("nightly-backup", async () => { await new Promise((r) => setTimeout(r, 15)); return 7; },
    { schedule: "0 2 * * *", tz: "Africa/Lagos", graceS: 600, maxRuntimeS: 3600 }), 7);
  await assert.rejects(lj.monitor("flaky", async () => { throw new Error("disk full for ann@example.com"); }, { capture: true }), /disk full/);
  lj.checkin("manual", "start", { runId: "r1" }); lj.checkin("bad name!", "ok");
  await lj.flush();
  const ci = items(s, "/checkin", "monitor") && s.got.filter((g) => g.path.endsWith("/checkin")).map((g) => g.body);
  const nb = ci.filter((b) => b.monitor === "nightly-backup");
  assert.deepEqual(nb.map((b) => b.status), ["start", "ok"]); assert.equal(nb[0].run_id, nb[1].run_id);
  assert.ok(nb[1].duration_ms >= 10); assert.equal(nb[0].schedule, "0 2 * * *"); assert.equal(nb[0].tz, "Africa/Lagos"); assert.equal(nb[0].grace_s, 600);
  const fl = ci.find((b) => b.monitor === "flaky" && b.status === "fail");
  assert.equal(fl.message, "Error: disk full for [email]");
  assert.ok(!ci.some((b) => b.monitor === "bad name!")); assert.equal(ci.at(-1).run_id, "r1");
  assert.ok(items(s, "/events", "events").some((e) => e.exception && e.exception.value === "disk full for [email]" || e.exception.value.startsWith("disk full")));
  done(s);
});

test("release and deploy markers", async () => {
  const s = await setup();
  lj.release({ commit: "abc1234", notes: "hotfix" });
  lj.deploy({ status: "success", url: "https://ci.example/1", notify: true });
  lj.deploy({ version: "2.0.1", environment: "staging", status: "failed" });
  await lj.flush();
  const rel = s.got.filter((g) => g.path.endsWith("/releases")).map((g) => g.body);
  const dep = s.got.filter((g) => g.path.endsWith("/deploys")).map((g) => g.body);
  assert.deepEqual(rel, [{ version: "2.0.0", commit: "abc1234", notes: "hotfix" }]);
  assert.deepEqual(dep[0], { version: "2.0.0", environment: "production", status: "success", notify: true, url: "https://ci.example/1" });
  assert.equal(dep[1].environment, "staging"); assert.equal(dep[1].notify, false);
  done(s);
});

test("retry on 429/503 then deliver, 413 splits a log batch", async () => {
  let s = await setup({}, [[429, { "retry-after": "0" }], [503, {}]]);
  lj.incr("retry.me"); assert.ok(await lj.flush(10000));
  assert.equal(items(s, "/metrics", "metrics")[0].name, "retry.me"); done(s);
  s = await setup({}, [[413, {}]]);
  for (let i = 0; i < 4; i++) lj.log("info", "l" + i);
  assert.ok(await lj.flush(5000));
  assert.deepEqual(items(s, "/logs", "logs").map((x) => x.message).sort(), ["l0", "l1", "l2", "l3"]); done(s);
});

test("queue stays bounded when the server is down", async () => {
  lj.init({ key: "lj_app_k", endpoint: "http://127.0.0.1:1", handlers: false, maxQueue: 5, retries: 0 });
  for (let i = 0; i < 300; i++) lj.log("info", "line " + i);
  assert.ok(lj._state.queue.length <= 5); assert.ok(lj._state.dropped > 0);
});

// ---------------------------------------------------------------- health checks

test("health registry grades values, booleans, errors and timeouts", async () => {
  const h = lj.health;
  h.register("db", async () => 50, { warn: 200, fail: 1000 });
  h.register("slow_db", () => 450, { warn: 200, fail: 1000 });
  h.register("dead_db", () => 5000, { warn: 200, fail: 1000 });
  h.register("disk_free", () => 3, { warn: 10, fail: 5, below: true });
  h.register("cache", () => true); h.register("down", () => false); h.register("boom", () => { throw new Error("nope"); });
  h.register("rich", () => ({ status: "warn", value: 7, message: "replica lag" }));
  h.register("hang", () => new Promise(() => {}), { timeoutMs: 100 });
  const body = await h.run();
  assert.deepEqual(Object.fromEntries(Object.entries(body.checks).map(([k, v]) => [k, v.status])),
    { db: "pass", slow_db: "warn", dead_db: "fail", disk_free: "fail", cache: "pass", down: "fail", boom: "fail", rich: "warn", hang: "fail" });
  assert.equal(body.status, "fail"); assert.equal(body.checks.db.value, 50); assert.match(body.checks.boom.message, /nope/); assert.match(body.checks.hang.message, /timed out/);
  assert.equal((await h.respond()).status, 503);
  for (const n of Object.keys(body.checks)) h.unregister(n);
});

test("health ok/degraded, token and http handler", async () => {
  const h = lj.health;
  h.register("a", () => true);
  assert.equal((await h.respond()).status, 200); assert.equal((await h.respond()).body.status, "ok");
  h.register("b", () => 300, { warn: 100 });
  const d = await h.respond(); assert.equal(d.status, 200); assert.equal(d.body.status, "degraded");
  assert.equal((await h.respond(null, "s3cret")).status, 401); assert.equal((await h.respond("Bearer wrong", "s3cret")).status, 401);
  assert.equal((await h.respond("Bearer s3cret", "s3cret")).status, 200);
  const srv = http.createServer(h.handler({ token: "t" }));
  await new Promise((r) => srv.listen(0, "127.0.0.1", r));
  const base = `http://127.0.0.1:${srv.address().port}`;
  assert.equal((await fetch(base)).status, 401);
  const ok = await fetch(base, { headers: { Authorization: "Bearer t" } });
  assert.equal(ok.status, 200); assert.equal(ok.headers.get("cache-control"), "no-store"); assert.equal((await ok.json()).checks.a.status, "pass");
  const rh = await h.routeHandler({ token: "t" })({ headers: new Headers({ authorization: "Bearer t" }) });
  assert.equal(rh.status, 200);
  srv.closeAllConnections(); srv.close();
  h.unregister("a"); h.unregister("b");
});

test("version", () => assert.equal(lj.SDK_VERSION, "1.3.0"));
