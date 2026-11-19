"use strict";
/**
 * Metrics, structured logs, trace spans, cron check-ins, release/deploy markers and health checks.
 * Same rules as the core: never throws into the host app, never blocks (everything goes onto the bounded queue),
 * personal data is scrubbed in-process before anything is sent.
 */
const crypto = require("node:crypto");
const { AsyncLocalStorage } = require("node:async_hooks");

module.exports = function build(I) {
  const { st, enqueue, scrubText, sensitiveKey, safe, clip, FILTERED, PATHS } = I;
  const NAME = /^[A-Za-z][A-Za-z0-9_.:/-]{0,99}$/;
  const RESERVOIR = 100;
  const LEVELS = { debug: 10, info: 20, warning: 30, warn: 30, error: 40, critical: 50, fatal: 50 };
  const LEVEL_NAME = { 10: "debug", 20: "info", 30: "warning", 40: "error", 50: "critical" };

  // ---------------------------------------------------------------- PII scrubbing
  const EMAIL = /[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63})+/g;
  const CARD = /(?<![\d-])(?:\d[ -]?){12,18}\d(?![\d-])/g;
  const JWT = /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*/g;
  const PREFIXED = /\b(?:lj_[a-z]{2,5}_[A-Za-z0-9_-]{8,}|ljp_[A-Za-z0-9_-]{8,}|ljr_[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,})\b/g;
  const luhn = (d) => { let t = 0; let alt = false; for (let i = d.length - 1; i >= 0; i--) { let n = +d[i]; if (alt) { n *= 2; if (n > 9) n -= 9; } t += n; alt = !alt; } return t % 10 === 0; };
  /** Replace emails, payment card numbers (Luhn-valid), JWTs, API keys and bearer tokens, plus `password=...` style text. */
  function scrubPii(text) {
    let t = scrubText(String(text));
    t = t.replace(JWT, "[token]").replace(PREFIXED, "[token]").replace(EMAIL, "[email]");
    return t.replace(CARD, (m) => { const d = m.replace(/\D/g, ""); return d.length >= 13 && d.length <= 19 && luhn(d) ? "[card]" : m; });
  }
  const clean = (v, n) => { const t = clip(typeof v === "string" ? v : v == null ? "" : String(v), n * 2); return clip(!st.opts || st.opts.piiScrub ? scrubPii(t) : t, n); };
  const mtags = (tags) => {
    const out = {};
    for (const [k, v] of Object.entries(tags || {}).slice(0, 10)) out[clip(k, 50)] = sensitiveKey(k) ? FILTERED : clean(v, 100);
    return out;
  };
  const nowIso = () => new Date().toISOString();

  // ---------------------------------------------------------------- metrics
  const metrics = new Map();
  let metricDrops = 0;
  let timer = null;
  function record(type, name, value, tags) {
    if (!st.opts || !st.key || st.disabled) return;
    if (typeof name !== "string" || !NAME.test(name)) return;
    const v = Number(value);
    if (!Number.isFinite(v)) return;
    const t = mtags(tags);
    const key = type + "\u0000" + name + "\u0000" + JSON.stringify(Object.entries(t).sort());
    let a = metrics.get(key);
    if (!a) {
      if (metrics.size >= st.opts.maxMetricSeries) { metricDrops++; return; } // bounded memory
      a = { type, name, tags: t, count: 0, sum: 0, min: v, max: v, last: v, values: [] };
      metrics.set(key, a);
      if (!timer) { timer = setInterval(flushMetrics, st.opts.metricsIntervalMs); timer.unref(); }
    }
    a.count++; a.sum += v; a.min = Math.min(a.min, v); a.max = Math.max(a.max, v); a.last = v;
    if (type === "histogram") {
      if (a.values.length < RESERVOIR) a.values.push(v);
      else { const j = Math.floor(Math.random() * a.count); if (j < RESERVOIR) a.values[j] = v; }
    }
  }
  const incr = safe(function incr(name, value = 1, tags) { record("counter", name, value, tags); });
  const gauge = safe(function gauge(name, value, tags) { record("gauge", name, value, tags); });
  const histogram = safe(function histogram(name, value, tags) { record("histogram", name, value, tags); });
  /** time("db.query", async () => ..., { table: "users" }) records milliseconds (also when the function throws). */
  async function time(name, fn, tags) {
    const t0 = process.hrtime.bigint();
    try { return await fn(); } finally { histogram(name, Number(process.hrtime.bigint() - t0) / 1e6, tags); }
  }
  /** const t = startTimer("job"); ...; t.end() */
  function startTimer(name, tags) {
    const t0 = process.hrtime.bigint(); let done = false;
    return { end() { if (!done) { done = true; histogram(name, Number(process.hrtime.bigint() - t0) / 1e6, tags); } } };
  }
  function flushMetrics() {
    if (!metrics.size) return 0;
    const ts = nowIso();
    const items = [...metrics.values()].map((a) => {
      const it = { name: a.name, type: a.type, tags: a.tags, timestamp: ts };
      if (a.type === "counter") it.value = a.sum;
      else if (a.type === "gauge") it.value = a.last;
      else Object.assign(it, { count: a.count, sum: a.sum, min: a.min, max: a.max, values: a.values });
      return it;
    });
    metrics.clear();
    for (let i = 0; i < items.length; i += 200) enqueue(PATHS.METRICS, { metrics: items.slice(i, i + 200) });
    return items.length;
  }

  // ---------------------------------------------------------------- tracing
  const als = new AsyncLocalStorage();
  const hex = (n) => crypto.randomBytes(n).toString("hex");
  function parseTraceparent(v) {
    const m = /^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$/.exec(String(v || "").trim().toLowerCase());
    return m ? { traceId: m[1], parentId: m[2], sampled: (parseInt(m[3], 16) & 1) === 1 } : null;
  }
  class Span {
    constructor(name, o = {}) {
      const parent = o.parent === undefined ? als.getStore() : o.parent;
      const tp = !parent && parseTraceparent(o.traceparent);
      this.name = clip(name, 200); this.kind = o.kind || "internal"; this.tags = { ...(o.tags || {}) };
      this.traceId = parent ? parent.traceId : tp ? tp.traceId : hex(16);
      this.parentId = parent ? parent.spanId : tp ? tp.parentId : "";
      this.sampled = parent ? parent.sampled : tp ? tp.sampled : !!st.opts && Math.random() < st.opts.tracesSampleRate;
      this.spanId = hex(8); this.status = "ok"; this.start = nowIso(); this.t0 = process.hrtime.bigint(); this.done = false;
    }
    setTag(k, v) { this.tags[String(k)] = v; return this; }
    unsampled() { this.sampled = false; return this; }
    traceparent() { return `00-${this.traceId}-${this.spanId}-${this.sampled ? "01" : "00"}`; }
    end(status, name, tags) {
      if (this.done) return;
      this.done = true;
      try {
        if (status) this.status = status;
        if (name) this.name = clip(name, 200);
        if (tags) Object.assign(this.tags, tags);
        if (this.sampled && st.opts) {
          enqueue(PATHS.SPANS, { trace_id: this.traceId, span_id: this.spanId, parent_id: this.parentId, name: this.name, kind: this.kind,
            start: this.start, duration_ms: Number(process.hrtime.bigint() - this.t0) / 1e6, status: this.status, tags: mtags(this.tags),
            release: st.release || "", environment: st.environment });
        }
      } catch (_) { /* tracing never breaks the app */ }
    }
  }
  const startSpan = (name, o) => { try { return new Span(name, o); } catch (_) { return new Span("noop", { parent: null }).unsampled(); } };
  const runWith = (span, fn) => als.run(span, fn);
  const currentSpan = () => als.getStore() || null;
  /** span("db.query", async (sp) => ..., { kind: "client", tags: {...} }): ends the span, marks it failed when fn throws. */
  async function span(name, fn, o) {
    const sp = startSpan(name, o);
    try { return await als.run(sp, () => fn(sp)); } catch (e) { sp.end("error"); throw e; } finally { sp.end(); }
  }
  /** Add the W3C traceparent header for an outgoing request so the downstream service joins this trace. */
  function injectHeaders(headers = {}) { const sp = currentSpan(); if (sp) headers.traceparent = sp.traceparent(); return headers; }

  // ---------------------------------------------------------------- logs
  const levelNo = (l) => (typeof l === "number" ? l : LEVELS[String(l).toLowerCase()] || 20);
  const logEvent = safe(function logEvent(level, message, context = {}, logger = "") {
    const o = st.opts;
    if (!o || !st.key || st.disabled) return;
    const no = levelNo(level);
    if (no < levelNo(o.logLevel)) return;
    if (o.sampleLogs < 1 && no < 40 && Math.random() >= o.sampleLogs) return;
    const ctx = {};
    for (const [k, v] of Object.entries(context || {}).slice(0, 30)) {
      ctx[clip(k, 50)] = sensitiveKey(k) ? FILTERED : typeof v === "number" || typeof v === "boolean" ? v : clean(typeof v === "object" ? JSON.stringify(v) : v, 300);
    }
    const sp = currentSpan();
    let item = { timestamp: nowIso(), level: LEVEL_NAME[Math.floor(no / 10) * 10] || "info", message: clean(message, 4000), logger: clip(logger, 100),
      context: ctx, release: st.release || "", environment: st.environment };
    if (sp) { item.trace_id = sp.traceId; item.span_id = sp.spanId; }
    if (typeof o.beforeSendLog === "function") { item = o.beforeSendLog(item); if (!item || typeof item !== "object") return; }
    enqueue(PATHS.LOGS, item);
  });
  const logs = {};
  for (const l of ["debug", "info", "warning", "error", "critical"]) logs[l] = (message, context, logger) => logEvent(l, message, context, logger);

  /** Forward console.log/info/warn/error to LINEARJEPH as well as the normal output. Returns an undo function. */
  function captureConsole(levels = ["info", "warn", "error"]) {
    const map = { log: "info", info: "info", warn: "warning", error: "error", debug: "debug" };
    const orig = {};
    for (const m of levels) {
      if (typeof console[m] !== "function") continue;
      orig[m] = console[m];
      console[m] = function (...a) {
        try { logEvent(map[m] || "info", a.map((x) => (typeof x === "string" ? x : x instanceof Error ? x.stack : JSON.stringify(x))).join(" "), {}, "console"); } catch (_) { /* never break logging */ }
        return orig[m].apply(this, a);
      };
    }
    return () => { for (const [m, f] of Object.entries(orig)) console[m] = f; };
  }
  /**
   * pino: `pino({}, multistream([{ stream: process.stdout }, { stream: linearjeph.pinoStream() }]))`
   * or `pino(linearjeph.pinoStream())`. Parses pino's JSON lines; extra fields become searchable context.
   */
  function pinoStream() {
    const PINO = { 10: "debug", 20: "debug", 30: "info", 40: "warning", 50: "error", 60: "critical" };
    return { write(chunk) {
      try {
        const o = JSON.parse(String(chunk));
        const { level, time: _t, msg, pid: _p, hostname: _h, name, v: _v, err, ...ctx } = o;
        logEvent(PINO[level] || "info", (msg || "") + (err && err.stack ? "\n" + err.stack : ""), ctx, name || "pino");
      } catch (_) { /* not JSON: ignore */ }
    } };
  }
  /** winston: `logger.add(new (linearjeph.winstonTransport())())` (needs winston, which provides winston-transport). */
  function winstonTransport() {
    const Transport = require("winston-transport"); // eslint-disable-line global-require
    return class LinearjephTransport extends Transport {
      log(info, cb) {
        try { const { level, message, timestamp: _t, splat: _s, ...ctx } = info; logEvent(level === "warn" ? "warning" : level, String(message), ctx, "winston"); } catch (_) { /* ignore */ }
        if (cb) cb();
      }
    };
  }

  // ---------------------------------------------------------------- check-ins
  const checkin = safe(function checkin(name, status = "ok", o = {}) {
    if (!I.HB_NAME.test(String(name))) { I.log("check-in name must match [A-Za-z0-9._-]{1,64}"); return; }
    const body = { monitor: String(name), status: ["start", "ok", "fail"].includes(status) ? status : "fail" };
    const map = { runId: "run_id", durationMs: "duration_ms", message: "message", schedule: "schedule", tz: "tz", graceS: "grace_s", intervalS: "interval_s", maxRuntimeS: "max_runtime_s" };
    for (const [k, wire] of Object.entries(map)) if (o[k] != null && o[k] !== "") body[wire] = k === "message" ? clean(o[k], 300) : o[k];
    enqueue(PATHS.CHECKIN, body);
  });
  /**
   * Cron wrapper: sends start, then ok (or fail with the error) with the duration, and re-throws failures.
   *   await monitor("nightly-backup", async () => {...}, { schedule: "0 2 * * *", graceS: 600, maxRuntimeS: 3600 })
   * Options: intervalS, schedule, tz, graceS, maxRuntimeS, capture (also report the error), flush (await delivery).
   */
  async function monitor(name, fn, o = {}) {
    const runId = crypto.randomUUID().replace(/-/g, "");
    const cfg = { intervalS: o.intervalS, schedule: o.schedule, tz: o.tz, graceS: o.graceS, maxRuntimeS: o.maxRuntimeS };
    const t0 = process.hrtime.bigint();
    checkin(name, "start", { runId, ...cfg });
    try {
      const r = await fn();
      checkin(name, "ok", { runId, durationMs: Number(process.hrtime.bigint() - t0) / 1e6, ...cfg });
      if (o.flush) await I.flush();
      return r;
    } catch (e) {
      if (o.capture) I.captureException(e);
      checkin(name, "fail", { runId, durationMs: Number(process.hrtime.bigint() - t0) / 1e6, message: `${e && e.name}: ${e && e.message}`, ...cfg });
      if (o.flush) await I.flush();
      throw e;
    }
  }

  // ---------------------------------------------------------------- releases and deploys
  const version = (v) => v || st.release || process.env.LINEARJEPH_RELEASE || "";
  const release = safe(function release(o = {}) {
    const v = version(o.version); if (!v || !st.opts) return;
    enqueue(PATHS.RELEASES, { version: v, ...(o.commit ? { commit: o.commit } : {}), ...(o.notes ? { notes: o.notes } : {}) });
  });
  const deploy = safe(function deploy(o = {}) {
    const v = version(o.version); if (!v || !st.opts) return;
    enqueue(PATHS.DEPLOYS, { version: v, environment: o.environment || st.environment, status: o.status || "success", notify: !!o.notify,
      ...(o.url ? { url: o.url } : {}), ...(o.commit ? { commit: o.commit } : {}) });
  });

  // ---------------------------------------------------------------- health checks
  const checks = new Map();
  const grade = (value, c) => {
    const worse = (lim) => (c.below ? value <= lim : value >= lim);
    if (c.fail != null && worse(c.fail)) return "fail";
    if (c.warn != null && worse(c.warn)) return "warn";
    return "pass";
  };
  const health = {
    /** register("db", async () => pingMs(), { warn: 200, fail: 1000 }). fn returns boolean, a number, or { status, value, message }. */
    register(name, fn, o = {}) { checks.set(String(name).slice(0, 60), { fn, warn: o.warn, fail: o.fail, below: !!o.below, timeout: o.timeoutMs || 5000 }); },
    unregister(name) { checks.delete(String(name)); },
    async runOne(c) {
      const t0 = process.hrtime.bigint();
      let to;
      try {
        const res = await Promise.race([Promise.resolve().then(c.fn), new Promise((_, rej) => { to = setTimeout(() => rej(new Error(`timed out after ${c.timeout} ms`)), c.timeout); })]);
        if (res && typeof res === "object") {
          const out = {}; for (const k of ["status", "value", "message"]) if (res[k] !== undefined) out[k] = res[k];
          if (!["pass", "warn", "fail"].includes(out.status)) out.status = typeof out.value === "number" ? grade(out.value, c) : "pass";
          return out;
        }
        if (typeof res === "number") return { status: grade(res, c), value: res };
        return { status: res === false ? "fail" : "pass", value: Math.round(Number(process.hrtime.bigint() - t0) / 1e5) / 10 };
      } catch (e) { return { status: "fail", message: `${(e && e.name) || "Error"}: ${e && e.message}`.slice(0, 200) }; } finally { clearTimeout(to); }
    },
    async run() {
      const entries = [...checks];
      const res = await Promise.all(entries.map(([, c]) => health.runOne(c)));
      const results = Object.fromEntries(entries.map(([n], i) => [n, res[i]]));
      const states = res.map((r) => r.status);
      return { status: states.includes("fail") ? "fail" : states.includes("warn") ? "degraded" : "ok", checks: results };
    },
    /** Framework neutral: resolves { status, body }. `token` requires `Authorization: Bearer <token>`. */
    async respond(authorization, token) {
      if (token) {
        const a = Buffer.from(String(authorization || "")); const b = Buffer.from("Bearer " + token);
        if (a.length !== b.length || !crypto.timingSafeEqual(a, b)) return { status: 401, body: { status: "unauthorized" } };
      }
      const body = await health.run();
      return { status: body.status === "fail" ? 503 : 200, body };
    },
    /** Node/Express/Connect handler: `app.get("/_lj/health", linearjeph.health.handler({ token }))`. */
    handler(o = {}) {
      return (req, res) => {
        health.respond(req.headers && req.headers.authorization, o.token).then(({ status, body }) => {
          res.statusCode = status; res.setHeader("Content-Type", "application/json"); res.setHeader("Cache-Control", "no-store");
          res.end(JSON.stringify(body));
        }, () => { res.statusCode = 500; res.end("{}"); });
      };
    },
    /** Next.js App Router: `export const GET = linearjeph.health.routeHandler({ token })`. */
    routeHandler(o = {}) {
      return async (request) => {
        const { status, body } = await health.respond(request.headers.get("authorization"), o.token);
        return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", "Cache-Control": "no-store" } });
      };
    },
  };

  // ---------------------------------------------------------------- Next.js API routes and route handlers
  /**
   * Wrap a Pages API route `(req, res)` or an App Router handler `(request, ctx) => Response` with a server span,
   * a transaction and error capture. `name` should be the route pattern, e.g. "GET /api/items/[id]".
   */
  function wrapNext(handler, o = {}) {
    return async function wrapped(req, res, ...rest) {
      const pages = res && typeof res.setHeader === "function";
      const method = (req && req.method) || "GET";
      const name = o.name || `${method} ${(pages ? req.url : req && req.url ? new URL(req.url, "http://x").pathname : "/") || "/"}`.split("?")[0];
      const hdr = pages ? req.headers && req.headers.traceparent : req && req.headers && typeof req.headers.get === "function" ? req.headers.get("traceparent") : null;
      const sp = startSpan(name, { kind: "server", traceparent: hdr });
      const t0 = process.hrtime.bigint();
      let status = 200;
      try {
        const out = await als.run(sp, () => handler(req, res, ...rest));
        status = pages ? res.statusCode || 200 : (out && out.status) || 200;
        return out;
      } catch (e) {
        status = 500; I.captureException(e); throw e;
      } finally {
        const bad = status >= 500;
        sp.end(bad ? "error" : "ok", name, { "http.status_code": String(status) });
        I.captureTransaction(name, Number(process.hrtime.bigint() - t0) / 1e6, bad ? "error" : "ok", { tags: { "http.status_code": String(status) } });
      }
    };
  }

  // ---------------------------------------------------------------- lifecycle
  function attach() {
    if (timer) { clearInterval(timer); timer = null; }
    metrics.clear(); metricDrops = 0;
    st.preFlush = flushMetrics;
  }

  return {
    attach,
    startSpan, runWith,
    api: { incr, gauge, histogram, time, startTimer, span, startSpan, currentSpan, injectHeaders, parseTraceparent, log: logEvent, logs, captureConsole,
      pinoStream, winstonTransport, checkin, monitor, release, deploy, health, wrapNext, scrubPii, flushMetrics, droppedMetricSeries: () => metricDrops },
  };
};
