"use strict";
/**
 * LINEARJEPH Node.js SDK. No runtime dependencies (global fetch, Node >= 18).
 * Rules: public functions never throw into the host app, sending is asynchronous with a bounded queue, and the
 * queue is flushed on beforeExit (and by flush()).
 */
const fs = require("node:fs");
const path = require("node:path");

const SDK_VERSION = "1.0.0";
const DEFAULT_ENDPOINT = "https://linearjeph.cc";
const EVENTS = "/ingest/v1/events";
const HEARTBEAT = "/ingest/v1/heartbeat";
const PACKAGES = "/ingest/v1/packages";
// Server limits (apps/api-core/errortrack, monitoring, audits)
const MAX_FRAMES = 50;
const MAX_TAGS = 20;
const BATCH_BYTES = 200 * 1024;
const FILTERED = "[Filtered]";
const HB_NAME = /^[A-Za-z0-9._-]{1,64}$/;
const PKG_NAME = /^[\w@./+~:-]{1,200}$/;
const PKG_VER = /^[\w.+~!:-]{1,100}$/;
const SENSITIVE_KEY = /(pass(word|wd)?|secret|token|authorization|auth|cookie|api[_-]?key|apikey|x-lj-key|private[_-]?key|credential|session|csrf|bearer)/i;
const SENSITIVE_TEXT = /\b(password|passwd|pwd|secret|token|api[_-]?key|apikey|authorization|access[_-]?key)\b(\s*[=:]\s*)(?:bearer\s+)?[^\s,;&'"]+/gi;
const BEARER = /\bbearer\s+[A-Za-z0-9._~+/=-]{8,}/gi;

const st = {
  opts: null, key: null, endpoint: DEFAULT_ENDPOINT, release: null, environment: "production",
  queue: [], draining: false, scheduled: false, disabled: false, exiting: false, dropped: 0, hooked: false,
  tags: {}, user: null,
  // legacy-visible knobs
  max: 100, retries: 4, backoff: 500,
};

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const log = (...a) => { if (st.opts && st.opts.debug) console.error("[linearjeph]", ...a); };
/** Wrap a public function so it can never throw into the host application. */
const safe = (fn) => function (...a) { try { return fn.apply(this, a); } catch (e) { log(fn.name, "failed:", e && e.message); return undefined; } };
const clip = (v, n) => (typeof v === "string" ? v : v == null ? "" : String(v)).slice(0, n);
const rate = (v, d = 1) => (typeof v === "number" && v >= 0 && v <= 1 ? v : d);

// ------------------------------------------------------------------ scrubbing
function sensitiveKey(k) {
  if (SENSITIVE_KEY.test(String(k))) return true;
  const extra = st.opts && st.opts.scrubKeys;
  return !!(extra && extra.some((x) => String(k).toLowerCase().includes(x)));
}
function scrubText(s) {
  return s.replace(SENSITIVE_TEXT, (_m, k, sep) => k + sep + FILTERED).replace(BEARER, "Bearer " + FILTERED);
}
/** Deep copy with values under sensitive keys (password, token, authorization, cookie, api_key, ...) replaced. */
function scrub(data, depth = 0) {
  if (depth > 8) return FILTERED;
  if (Array.isArray(data)) return data.map((v) => scrub(v, depth + 1));
  if (data && typeof data === "object") {
    const out = {};
    for (const [k, v] of Object.entries(data)) out[k] = sensitiveKey(k) ? FILTERED : scrub(v, depth + 1);
    return out;
  }
  return typeof data === "string" ? scrubText(data) : data;
}
function cleanTags(tags) {
  const out = {};
  for (const [k, v] of Object.entries(tags || {}).slice(0, MAX_TAGS)) {
    out[clip(k, 50)] = sensitiveKey(k) ? FILTERED : clip(scrubText(typeof v === "string" ? v : String(v)), 200);
  }
  return out;
}

// ------------------------------------------------------------------ events
function parseStack(stack) {
  const frames = [];
  for (const line of String(stack || "").split("\n").slice(1)) {
    const m = /^\s*at (?:(.*?) \()?(.*?):(\d+):\d+\)?$/.exec(line);
    if (!m) continue;
    const filename = m[2].replace(/^file:\/\//, "");
    frames.push({ filename, function: m[1] || "<anonymous>", lineno: Number(m[3]),
      in_app: !filename.includes("node_modules") && !filename.startsWith("node:") });
  }
  return frames.reverse().slice(-MAX_FRAMES); // oldest first, like the ingest expects
}

function serializeError(err) {
  const e = err instanceof Error ? err : new Error(typeof err === "string" ? err : safeJson(err));
  return { type: "error", timestamp: new Date().toISOString(), release: st.release || "", environment: st.environment,
    exception: { type: String(e.name || "Error").slice(0, 200), value: scrubText(String(e.message)).slice(0, 500),
      stacktrace: parseStack(e.stack) }, tags: {}, user: {} };
}
function safeJson(v) { try { return JSON.stringify(v); } catch (_) { return String(v); } }

/** Sampling, scope tags, scrubbing, beforeSend. Returns the event or null. */
function finish(ev, tags, sampleRate) {
  const o = st.opts;
  if (!o || !st.key) return null;
  if (sampleRate < 1 && Math.random() >= sampleRate) return null;
  ev.tags = cleanTags({ ...st.tags, ...(tags || {}), ...(ev.tags || {}) });
  ev.user = st.user ? { id: clip(st.user, 100) } : {};
  if (typeof o.beforeSend === "function") {
    try { ev = o.beforeSend(ev); } catch (e) { log("beforeSend threw, event dropped"); return null; }
    if (!ev || typeof ev !== "object") return null;
    ev.tags = cleanTags(ev.tags);
  }
  return ev;
}
function emit(ev, tags, sampleRate) {
  if (!st.opts) return;
  const e = finish(ev, tags, sampleRate === undefined ? st.opts.sampleRate : sampleRate);
  if (e) enqueue(EVENTS, e);
}

// ------------------------------------------------------------------ transport
/** @returns {Promise<"ok"|"drop"|"split">} */
async function post(p, body) {
  const url = st.endpoint.replace(/\/$/, "") + p;
  const retries = st.exiting ? 0 : st.retries;
  for (let i = 0; i <= retries; i++) {
    let wait = null;
    try {
      const res = await fetch(url, { method: "POST", body: JSON.stringify(body),
        headers: { "Content-Type": "application/json", "X-LJ-Key": st.key, "User-Agent": "linearjeph-node/" + SDK_VERSION },
        signal: AbortSignal.timeout(st.opts ? st.opts.timeoutMs : 10000) });
      try { await res.arrayBuffer(); } catch (_) { /* body not needed */ }
      if (res.ok) return "ok";
      if (res.status === 401) { st.disabled = true; console.error("[linearjeph] ingest key rejected (401); sending disabled"); return "drop"; }
      if (res.status === 413) return body && Array.isArray(body.events) && body.events.length > 1 ? "split" : "drop";
      if (![429, 500, 502, 503, 504].includes(res.status)) { log(p, "->", res.status, "dropped"); return "drop"; }
      if (res.status === 429) {
        const ra = Number(res.headers.get("retry-after"));
        if (ra > 0) wait = Math.min(ra, 60) * 1000;
      }
    } catch (_) { /* network error or timeout: retry */ }
    if (i < retries) await sleep(wait != null ? wait : st.backoff * 2 ** i * (0.5 + Math.random() / 2));
  }
  log("gave up sending to", p);
  return "drop";
}

async function sendEvents(events) {
  if (st.disabled) return;
  if ((await post(EVENTS, { events })) === "split") {
    const mid = Math.ceil(events.length / 2);
    await sendEvents(events.slice(0, mid));
    await sendEvents(events.slice(mid));
  }
}

async function drain() {
  if (st.draining) return;
  st.draining = true;
  try {
    while (st.queue.length && !st.disabled) {
      const item = st.queue.shift();
      if (item.path !== EVENTS) { await post(item.path, item.body); continue; }
      const batch = [item.body];
      let size = JSON.stringify(item.body).length;
      while (batch.length < st.opts.batchSize && size < BATCH_BYTES) {
        const i = st.queue.findIndex((q) => q.path === EVENTS);
        if (i < 0) break;
        const [n] = st.queue.splice(i, 1);
        batch.push(n.body);
        size += JSON.stringify(n.body).length;
      }
      await sendEvents(batch);
    }
    if (st.disabled) st.queue.length = 0;
  } catch (e) { log("drain error", e && e.message); }
  finally { st.draining = false; }
}

function enqueue(p, body) {
  if (!st.key || st.disabled || !st.opts) return;
  if (st.queue.length >= st.max) { st.dropped++; return; } // bounded: never grow without limit
  st.queue.push({ path: p, body });
  if (!st.scheduled && !st.draining) {
    st.scheduled = true;
    setTimeout(() => { st.scheduled = false; void drain(); }, 20); // tiny delay so a burst becomes one batch
  }
}

/** Wait until the queue is delivered (or timeoutMs). Resolves true when empty. */
async function flush(timeoutMs = 5000) {
  const end = Date.now() + timeoutMs;
  if (st.queue.length && !st.draining) void drain();
  while ((st.queue.length || st.draining) && !st.disabled && Date.now() < end) await sleep(10);
  return st.disabled || (st.queue.length === 0 && !st.draining);
}

// ------------------------------------------------------------------ public API
function init(opts) {
  try {
    const o = typeof opts === "string" ? { key: opts } : opts || {};
    if (!o.key || typeof o.key !== "string") { console.error("[linearjeph] init() without a key; SDK disabled"); return false; }
    st.opts = {
      sampleRate: rate(o.sampleRate), tracesSampleRate: rate(o.tracesSampleRate), beforeSend: o.beforeSend,
      batchSize: Math.max(1, Math.min(o.batchSize || 20, 100)), timeoutMs: o.timeoutMs || 10000, debug: !!o.debug,
      scrubKeys: (o.scrubKeys || []).map((x) => String(x).toLowerCase()), flushTimeoutMs: o.flushTimeoutMs || 5000,
    };
    st.key = o.key;
    st.endpoint = o.endpoint || DEFAULT_ENDPOINT;
    st.release = o.release ?? null;
    st.environment = o.environment || "production";
    st.max = Math.max(1, o.maxQueue || 100);
    st.retries = o.retries !== undefined ? o.retries : 4;
    st.backoff = o.backoffMs !== undefined ? o.backoffMs : 500;
    st.disabled = false; st.exiting = false; st.queue.length = 0; st.dropped = 0; st.tags = {}; st.user = null;
    if (o.handlers !== false && !st.hooked) {
      st.hooked = true;
      installHandlers(o.exitOnUncaught !== false);
    }
    return true;
  } catch (e) { return false; }
}

function installHandlers(exitOnUncaught) {
  // Default Node behaviour turns an unhandled rejection into an uncaughtException, so one hook covers both.
  // We do not add an 'unhandledRejection' listener: that would silently disable Node's default crash.
  const solo = process.listenerCount("uncaughtException") === 0;
  if (solo && exitOnUncaught) {
    process.on("uncaughtException", async (err, origin) => {
      try {
        console.error(err && err.stack ? err.stack : err);
        captureException(err, { tags: { origin: String(origin) } });
        await flush(2000);
      } finally { process.exit(1); }
    });
  } else {
    process.on("uncaughtExceptionMonitor", (err, origin) => { captureException(err, { tags: { origin: String(origin) } }); });
  }
  process.on("beforeExit", () => { st.exiting = true; void flush(st.opts ? st.opts.flushTimeoutMs : 5000); });
}

/** captureException(err, { tags }) */
const captureException = safe(function captureException(err, ctx) {
  emit(serializeError(err), ctx && ctx.tags);
});
const captureMessage = safe(function captureMessage(msg, level = "info", ctx) {
  emit({ type: "error", timestamp: new Date().toISOString(), release: st.release || "", environment: st.environment,
    exception: { type: "Message", value: scrubText(String(msg)).slice(0, 500), stacktrace: [] }, tags: { level: String(level) }, user: {} },
  ctx && ctx.tags);
});
const captureTransaction = safe(function captureTransaction(name, durationMs, status = "ok", ctx) {
  const d = Number(durationMs);
  if (!Number.isFinite(d) || d < 0) return;
  emit({ type: "transaction", timestamp: new Date().toISOString(), release: st.release || "", environment: st.environment,
    transaction: { name: clip(name, 200), duration_ms: d, status: clip(status, 20) || "ok" }, tags: {}, user: {} },
  ctx && ctx.tags, st.opts ? st.opts.tracesSampleRate : 1);
});
const setTag = safe(function setTag(key, value) { st.tags[String(key)] = value; });
const setUser = safe(function setUser(id) { st.user = id == null || id === "" ? null : String(id); });

/** const t = startTransaction("job"); ...; t.end("ok") — or use withTransaction(). */
function startTransaction(name) {
  const t0 = process.hrtime.bigint();
  let done = false;
  return { end(status = "ok") {
    if (done) return;
    done = true;
    captureTransaction(name, Number(process.hrtime.bigint() - t0) / 1e6, status);
  } };
}
/** Time an async/sync function; status "error" if it throws/rejects (the error is re-thrown, not captured). */
async function withTransaction(name, fn) {
  const t = startTransaction(name);
  try { const r = await fn(); t.end("ok"); return r; } catch (e) { t.end("error"); throw e; }
}

const heartbeat = safe(function heartbeat(name, status = "ok", intervalS) {
  if (!HB_NAME.test(String(name))) { log("heartbeat name must match [A-Za-z0-9._-]{1,64}"); return; }
  const body = { name: String(name), status: status === "ok" ? "ok" : "error" };
  if (intervalS) body.interval_s = Math.max(30, Math.min(Math.floor(intervalS), 30 * 86400));
  enqueue(HEARTBEAT, body);
});
/**
 * Cron wrapper: monitor("nightly", async () => {...}, { intervalS: 86400 }) sends ok/error and re-throws on failure.
 * Pass { capture: true } to also report the exception, { flush: true } to await delivery before returning.
 */
async function monitor(name, fn, opts = {}) {
  try {
    const r = await fn();
    heartbeat(name, "ok", opts.intervalS);
    if (opts.flush) await flush();
    return r;
  } catch (e) {
    if (opts.capture) captureException(e);
    heartbeat(name, "error", opts.intervalS);
    if (opts.flush) await flush();
    throw e;
  }
}

function packagesFromLock(lock) {
  const out = new Map();
  for (const [p, v] of Object.entries((lock && lock.packages) || {})) {
    if (!p || !v || !v.version || v.link) continue;
    const i = p.lastIndexOf("node_modules/");
    out.set(v.name || p.slice(i + 13), v.version);
  }
  const walk = (deps) => {
    for (const [n, v] of Object.entries(deps || {})) {
      if (v && v.version && !out.has(n)) out.set(n, v.version);
      if (v && v.dependencies) walk(v.dependencies);
    }
  };
  walk(lock && lock.dependencies); // lockfileVersion 1
  return [...out].filter(([n, v]) => PKG_NAME.test(n) && PKG_VER.test(v)).map(([name, version]) => ({ name, version }));
}
/** Sends package-lock.json dependencies (npm) to the dependency audit. Returns the number queued (0 on any problem). */
const reportPackages = safe(function reportPackages(lockPath = path.join(process.cwd(), "package-lock.json")) {
  let all;
  try { all = packagesFromLock(JSON.parse(fs.readFileSync(lockPath, "utf8"))); } catch (e) { log("cannot read", lockPath); return 0; }
  const packages = []; let size = 64;
  for (const p of all) { // the server replaces the whole list per request: one request, capped by size and count
    size += p.name.length + p.version.length + 30;
    if (packages.length >= 5000 || size > BATCH_BYTES) break;
    packages.push(p);
  }
  enqueue(PACKAGES, { ecosystem: "npm", packages });
  return packages.length;
});

// ------------------------------------------------------------------ Express / Connect
/** Express: app.use(requestHandler()) first, app.use(errorHandler()) after the routes. Option: { ignorePaths: ["/health"] } */
function requestHandler(o = {}) {
  const ignore = o.ignorePaths || [];
  return (req, res, next) => {
    const t0 = process.hrtime.bigint();
    res.on("finish", () => {
      try {
        const p = req.originalUrl ? String(req.originalUrl).split("?")[0] : req.path || "/";
        if (ignore.some((x) => p.startsWith(x))) return;
        const route = req.route && req.route.path !== undefined ? (req.baseUrl || "") + req.route.path : req.path || "/";
        captureTransaction(`${req.method} ${route || "/"}`, Number(process.hrtime.bigint() - t0) / 1e6,
          res.statusCode < 500 ? "ok" : "error", { tags: { "http.status_code": String(res.statusCode) } });
      } catch (_) { /* never break a response */ }
    });
    next();
  };
}
function errorHandler() {
  return (err, _req, _res, next) => { captureException(err); next(err); };
}

module.exports = {
  SDK_VERSION, init, flush, scrub, setTag, setUser,
  captureException, captureMessage, captureTransaction, startTransaction, withTransaction,
  heartbeat, monitor, reportPackages, packagesFromLock,
  requestHandler, errorHandler, timing: requestHandler, expressErrorHandler: errorHandler,
  serializeError, _state: st,
};
