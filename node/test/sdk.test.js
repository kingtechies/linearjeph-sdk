const test = require("node:test");
const assert = require("node:assert");
const http = require("node:http");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { EventEmitter } = require("node:events");
const lj = require("../index.cjs");

/** Local ingest stand-in. `script` is a list of [status, headers] answered first, then 202. */
function server(script = []) {
  const got = []; const hits = []; const queue = [...script];
  const srv = http.createServer((req, res) => {
    let b = ""; req.on("data", (c) => (b += c)); req.on("end", () => {
      if (queue.length) { const [code, headers] = queue.shift(); hits.push(code); res.writeHead(code, headers || {}); return res.end(); }
      hits.push(202);
      got.push({ path: req.url, key: req.headers["x-lj-key"], body: JSON.parse(b) });
      res.statusCode = 202; res.end('{"accepted":1}');
    });
  });
  return new Promise((r) => srv.listen(0, "127.0.0.1", () => r({ srv, got, hits, url: `http://127.0.0.1:${srv.address().port}` })));
}
const events = (s) => s.got.filter((g) => g.body.events).flatMap((g) => g.body.events);
const setup = async (script, extra = {}) => {
  const s = await server(script);
  lj.init({ key: "lj_app_k", endpoint: s.url, release: "1.2.3", handlers: false, backoffMs: 5, ...extra });
  return s;
};

test("captureException payload matches the ingest contract", async () => {
  const s = await setup();
  lj.captureException(new TypeError("boom"));
  assert.ok(await lj.flush());
  const ev = s.got[0].body.events[0];
  assert.equal(s.got[0].key, "lj_app_k"); assert.equal(s.got[0].path, "/ingest/v1/events");
  assert.equal(ev.type, "error"); assert.equal(ev.release, "1.2.3"); assert.equal(ev.environment, "production");
  assert.equal(ev.exception.type, "TypeError");
  const f = ev.exception.stacktrace.at(-1);
  assert.deepEqual(Object.keys(f).sort(), ["filename", "function", "in_app", "lineno"]);
  assert.equal(f.in_app, true);
  s.srv.close();
});

test("retries on 503 and succeeds", async () => {
  const s = await setup([[503], [503]]);
  lj.captureMessage("hi");
  assert.ok(await lj.flush());
  assert.deepEqual(s.hits, [503, 503, 202]);
  s.srv.close();
});

test("429 honours Retry-After", async () => {
  const s = await setup([[429, { "Retry-After": "0" }]]);
  lj.captureMessage("later");
  assert.ok(await lj.flush());
  assert.deepEqual(s.hits, [429, 202]);
  s.srv.close();
});

test("401 disables sending", async () => {
  const s = await setup([[401]]);
  const orig = console.error; console.error = () => {};
  lj.captureMessage("a");
  await lj.flush();
  lj.captureMessage("b");
  await new Promise((r) => setTimeout(r, 80));
  console.error = orig;
  assert.deepEqual(s.hits, [401]); assert.equal(lj._state.disabled, true);
  s.srv.close();
});

test("400 and 413 are dropped without retry; gives up after retries without throwing", async () => {
  const s = await setup([[413], [400]]);
  lj.captureMessage("a"); await lj.flush();
  lj.captureMessage("b"); await lj.flush();
  assert.deepEqual(s.hits, [413, 400]);
  s.srv.close();
  const s2 = await setup(Array(10).fill([500]), { retries: 2 });
  lj.captureMessage("x");
  assert.ok(await lj.flush());
  assert.deepEqual(s2.hits, [500, 500, 500]);
  s2.srv.close();
});

test("unreachable endpoint never throws", async () => {
  lj.init({ key: "k", endpoint: "http://127.0.0.1:9", handlers: false, retries: 1, backoffMs: 5 });
  lj.captureMessage("nobody home");
  assert.ok(await lj.flush());
});

test("queue is bounded and batches events", async () => {
  const s = await setup([], { maxQueue: 5, batchSize: 50 });
  for (let i = 0; i < 8; i++) lj.captureMessage("m" + i);
  assert.equal(lj._state.dropped, 3);
  assert.ok(await lj.flush());
  assert.equal(events(s).length, 5); assert.equal(s.got.length, 1);
  s.srv.close();
});

test("heartbeat, transaction and packages from lock", async () => {
  const s = await setup();
  lj.heartbeat("job", "ok", 60);
  lj.captureTransaction("GET /x", 12.5);
  const pk = lj.packagesFromLock({ packages: { "": { name: "x" }, "node_modules/a": { version: "1.0.0" },
    "node_modules/a/node_modules/@s/b": { version: "2.0.0" }, "node_modules/l": { link: true }, "node_modules/al": { name: "real", version: "3.0.0" } } });
  assert.deepEqual(pk, [{ name: "a", version: "1.0.0" }, { name: "@s/b", version: "2.0.0" }, { name: "real", version: "3.0.0" }]);
  assert.ok(await lj.flush());
  const by = Object.fromEntries(s.got.map((g) => [g.path, g.body]));
  assert.deepEqual(by["/ingest/v1/heartbeat"], { name: "job", status: "ok", interval_s: 60 });
  assert.equal(by["/ingest/v1/events"].events[0].transaction.duration_ms, 12.5);
  s.srv.close();
});

test("lockfile v1 nested dependencies and reportPackages from a file", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ljsdk-"));
  const f = path.join(dir, "package-lock.json");
  fs.writeFileSync(f, JSON.stringify({ lockfileVersion: 1, dependencies: { a: { version: "1.0.0", dependencies: { b: { version: "2.0.0" } } } } }));
  const s = await setup();
  assert.equal(lj.reportPackages(f), 2);
  assert.equal(lj.reportPackages(path.join(dir, "missing.json")), 0);
  assert.ok(await lj.flush());
  assert.deepEqual(s.got[0].body, { ecosystem: "npm", packages: [{ name: "a", version: "1.0.0" }, { name: "b", version: "2.0.0" }] });
  s.srv.close(); fs.rmSync(dir, { recursive: true });
});

test("heartbeat validation", async () => {
  const s = await setup();
  lj.heartbeat("bad name!", "ok"); lj.heartbeat("x".repeat(65), "ok"); lj.heartbeat("job", "weird", 5);
  assert.ok(await lj.flush());
  assert.deepEqual(s.got.map((g) => g.body), [{ name: "job", status: "error", interval_s: 30 }]);
  s.srv.close();
});

test("monitor sends ok/error and rethrows", async () => {
  const s = await setup();
  assert.equal(await lj.monitor("good", async () => 7, { intervalS: 3600 }), 7);
  await assert.rejects(lj.monitor("bad", async () => { throw new Error("nope"); }, { capture: true }), /nope/);
  assert.ok(await lj.flush());
  const hb = Object.fromEntries(s.got.filter((g) => g.path.endsWith("heartbeat")).map((g) => [g.body.name, g.body]));
  assert.equal(hb.good.status, "ok"); assert.equal(hb.good.interval_s, 3600); assert.equal(hb.bad.status, "error");
  assert.ok(events(s).some((e) => e.exception.value === "nope"));
  s.srv.close();
});

test("startTransaction and withTransaction", async () => {
  const s = await setup();
  const t = lj.startTransaction("block"); await new Promise((r) => setTimeout(r, 15)); t.end(); t.end();
  await assert.rejects(lj.withTransaction("bad", () => { throw new Error("x"); }));
  assert.equal(await lj.withTransaction("good", async () => 5), 5);
  assert.ok(await lj.flush());
  const tx = events(s).map((e) => e.transaction);
  assert.equal(tx.filter((x) => x.name === "block").length, 1);
  assert.ok(tx.find((x) => x.name === "block").duration_ms >= 10);
  assert.equal(tx.find((x) => x.name === "bad").status, "error");
  s.srv.close();
});

test("scrubbing of tags and message text", async () => {
  const s = await setup([], { scrubKeys: ["ssn"] });
  lj.setTag("password", "hunter2"); lj.setTag("Authorization", "Bearer abcdefghijkl"); lj.setTag("ssn", "1"); lj.setTag("region", "eu");
  lj.captureMessage("failed password=hunter2 Bearer abcdefghijklmnop", "info", { tags: { api_key: "k" } });
  assert.ok(await lj.flush());
  const ev = events(s)[0];
  for (const k of ["password", "Authorization", "ssn", "api_key"]) assert.equal(ev.tags[k], "[Filtered]");
  assert.equal(ev.tags.region, "eu");
  assert.ok(!JSON.stringify(ev).includes("hunter2") && !JSON.stringify(ev).includes("abcdefghijklmnop"));
  assert.deepEqual(lj.scrub({ headers: { Cookie: "a=b", ok: 1 }, l: [{ token: "t" }] }), { headers: { Cookie: "[Filtered]", ok: 1 }, l: [{ token: "[Filtered]" }] });
  s.srv.close();
});

test("beforeSend modifies and drops; throwing drops", async () => {
  const s = await setup([], { beforeSend: (e) => (e.type === "transaction" ? null : { ...e, tags: { ...e.tags, seen: "yes" } }) });
  lj.captureTransaction("t", 1); lj.captureMessage("kept");
  assert.ok(await lj.flush());
  assert.equal(events(s).length, 1); assert.equal(events(s)[0].tags.seen, "yes");
  lj.init({ key: "k", endpoint: s.url, handlers: false, beforeSend: () => { throw new Error("x"); } });
  lj.captureMessage("lost"); assert.ok(await lj.flush());
  assert.equal(events(s).length, 1);
  s.srv.close();
});

test("sampling rates are independent for errors and transactions", async () => {
  const s = await setup([], { sampleRate: 0, tracesSampleRate: 1 });
  lj.captureMessage("dropped"); lj.captureTransaction("kept", 1);
  assert.ok(await lj.flush());
  assert.deepEqual(events(s).map((e) => e.type), ["transaction"]);
  s.srv.close();
});

test("setUser and tag limits", async () => {
  const s = await setup();
  lj.setUser("u-42");
  for (let i = 0; i < 30; i++) lj.setTag("k" + i, "v".repeat(300));
  lj.captureMessage("m"); assert.ok(await lj.flush());
  const ev = events(s)[0];
  assert.deepEqual(ev.user, { id: "u-42" }); assert.equal(Object.keys(ev.tags).length, 20);
  assert.ok(Object.values(ev.tags).every((v) => v.length <= 200));
  s.srv.close();
});

test("never throws without init or with bad input", async () => {
  lj.init({ key: "" });
  lj.captureException(undefined); lj.captureTransaction("n", "NaN"); lj.heartbeat(undefined);
  assert.equal(lj.reportPackages("/nonexistent"), 0);
  lj.captureException({ circular: (() => { const o = {}; o.o = o; return o; })() });
});

test("express middlewares", async () => {
  const s = await setup();
  const res = new EventEmitter(); res.statusCode = 200;
  let called = false;
  lj.requestHandler()({ method: "GET", path: "/p", originalUrl: "/p?x=1", baseUrl: "/api", route: { path: "/p/:id" } }, res, () => { called = true; });
  res.emit("finish");
  const skip = new EventEmitter(); skip.statusCode = 200;
  lj.requestHandler({ ignorePaths: ["/health"] })({ method: "GET", path: "/health", originalUrl: "/health" }, skip, () => {});
  skip.emit("finish");
  let passed;
  lj.errorHandler()(new Error("x"), {}, {}, (e) => { passed = e; });
  assert.ok(called && passed instanceof Error);
  assert.ok(await lj.flush());
  const evs = events(s);
  assert.ok(evs.some((e) => e.type === "transaction" && e.transaction.name === "GET /api/p/:id" && e.tags["http.status_code"] === "200"));
  assert.ok(!evs.some((e) => e.type === "transaction" && e.transaction.name.includes("/health")));
  assert.ok(evs.some((e) => e.type === "error"));
  assert.equal(lj.expressErrorHandler, lj.errorHandler);
  s.srv.close();
});

test("ESM entry exports the same API", async () => {
  const m = await import("../index.mjs");
  assert.equal(typeof m.init, "function"); assert.equal(m.captureException, lj.captureException);
  assert.equal(m.monitor, lj.monitor); assert.equal(m.default, lj);
});

test("uncaught exception handler captures, flushes and exits 1 (child process)", async () => {
  const s = await server();
  const code = `const lj=require(${JSON.stringify(path.join(__dirname, "..", "index.cjs"))});
    lj.init({key:"lj_app_k",endpoint:${JSON.stringify(s.url)}}); setTimeout(()=>{throw new Error("crash!")},10);`;
  const r = await new Promise((res) => {
    const c = require("node:child_process").spawn(process.execPath, ["-e", code], { stdio: "pipe" });
    let err = ""; c.stderr.on("data", (d) => (err += d)); c.on("exit", (status) => res({ status, err }));
  });
  assert.equal(r.status, 1); assert.match(r.err, /crash!/);
  assert.ok(events(s).some((e) => e.exception.value === "crash!"));
  s.srv.close();
});
