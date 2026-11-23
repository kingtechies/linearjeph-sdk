"use strict";
/**
 * LINEARJEPH Prophecy: growth-intelligence tracking, codebase scan, history import and report access.
 * Zero dependencies. Tracking calls never throw into the host app; secret-key calls refuse to run in a browser.
 */
const crypto = require("node:crypto");
const zlib = require("node:zlib");

const SDK_VERSION = "1.3.0";
const DEFAULT_ENDPOINT = "https://linearjeph.cc";
const P_EVENTS = "/ingest/v1/prophecy/events";
const P_IDENTIFY = "/ingest/v1/prophecy/identify";
const P_MANIFEST = "/ingest/v1/prophecy/manifest";
const A_IMPORT = "/api/v1/prophecy/sdk/import";
const A_REPORT = "/api/v1/prophecy/sdk/report";
const A_REFRESH = "/api/v1/prophecy/sdk/refresh";
const A_LIVE = "/api/v1/prophecy/sdk/live";
const A_EMBED = "/api/v1/prophecy/sdk/embed";
const MAX_QUEUE = 10000, BATCH = 100, INTERVAL_MS = 2000, IMPORT_BATCH = 5000, IMPORT_BYTES = 4.5 * 1024 * 1024, MANIFEST_BYTES = 500 * 1024;
const KINDS = new Set(["signup", "login", "active", "feature", "payment", "refund", "subscription", "churn", "pageview", "request", "error", "custom"]);

const st = { ingestKey: null, secretKey: null, endpoint: DEFAULT_ENDPOINT, hash: true, queue: [], timer: null, draining: false, disabled: false,
  exiting: false, dropped: 0, hooked: false, debug: false, retries: 4, backoff: 500, timeoutMs: 10000, batch: BATCH, warned: new Set() };

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const clip = (v, n) => (typeof v === "string" ? v : v == null ? "" : String(v)).slice(0, n);
/** Log a problem once per message so a failing backend never floods the host's logs. */
function warnOnce(msg) { if (st.warned.has(msg)) return; st.warned.add(msg); try { console.error("[linearjeph/prophecy] " + msg); } catch (_) { /* ignore */ } }
const safe = (fn) => function (...a) { try { return fn.apply(this, a); } catch (e) { warnOnce(fn.name + " failed: " + (e && e.message)); return undefined; } };
const inBrowser = () => typeof window !== "undefined";
const sha256 = (s) => crypto.createHash("sha256").update(String(s)).digest("hex");
const uidOf = (uid) => (uid == null || uid === "" ? "" : st.hash ? sha256(uid) : clip(uid, 200));

function init(opts) {
  try {
    const o = opts || {};
    const env = typeof process !== "undefined" && process.env ? process.env : {};
    const ingestKey = o.ingestKey || env.LINEARJEPH_INGEST_KEY || null;
    const secretKey = o.secretKey || env.LINEARJEPH_SECRET_KEY || null;
    if (ingestKey && String(ingestKey).startsWith("ljp_sk_")) { console.error("[linearjeph/prophecy] ingestKey looks like a secret key (ljp_sk_...); use the application ingest key. Tracking disabled."); return false; }
    st.ingestKey = ingestKey ? String(ingestKey) : null;
    st.secretKey = secretKey && !inBrowser() ? String(secretKey) : null;
    if (secretKey && inBrowser()) console.error("[linearjeph/prophecy] secretKey ignored: secret keys must never be used in a browser.");
    st.endpoint = String(o.endpoint || env.LINEARJEPH_ENDPOINT || DEFAULT_ENDPOINT).replace(/\/$/, "");
    st.hash = o.hashUserIds !== false;
    st.retries = o.retries !== undefined ? o.retries : 4;
    st.backoff = o.backoffMs !== undefined ? o.backoffMs : 500;
    st.timeoutMs = o.timeoutMs || 10000;
    st.batch = Math.max(1, Math.min(o.batchSize || BATCH, 500));
    st.debug = !!o.debug;
    st.queue.length = 0; st.dropped = 0; st.disabled = false; st.exiting = false; st.warned.clear();
    if (st.timer) clearInterval(st.timer);
    st.timer = null;
    if (st.ingestKey) {
      st.timer = setInterval(() => { void drain(); }, o.flushIntervalMs || INTERVAL_MS);
      if (st.timer.unref) st.timer.unref();
      if (o.handlers !== false && !st.hooked) {
        st.hooked = true;
        process.on("beforeExit", () => { st.exiting = true; void flush(5000); });
      }
    }
    return !!(st.ingestKey || st.secretKey);
  } catch (e) { return false; }
}

// ------------------------------------------------------------------ transport
function url(p) { return st.endpoint + p; }
/** @returns {Promise<"ok"|"drop">} */
async function postIngest(p, body) {
  const retries = st.exiting ? 0 : st.retries;
  let data = zlib.gzipSync(JSON.stringify(body));
  for (let i = 0; i <= retries; i++) {
    let wait = null;
    try {
      const res = await fetch(url(p), { method: "POST", body: data, signal: AbortSignal.timeout(st.timeoutMs),
        headers: { "Content-Type": "application/json", "Content-Encoding": "gzip", "X-LJ-Key": st.ingestKey, "User-Agent": "linearjeph-node/" + SDK_VERSION } });
      try { await res.arrayBuffer(); } catch (_) { /* body unused */ }
      if (res.ok) return "ok";
      if (res.status === 401) { st.disabled = true; warnOnce("ingest key rejected (401); tracking disabled"); return "drop"; }
      if (res.status === 402) { warnOnce("Prophecy subscription inactive (402); events dropped"); return "drop"; }
      if (![429, 500, 502, 503, 504].includes(res.status)) { warnOnce(p + " answered " + res.status + "; batch dropped"); return "drop"; }
      const ra = Number(res.headers.get("retry-after"));
      if (res.status === 429 && ra > 0) wait = Math.min(ra, 60) * 1000;
    } catch (_) { /* network error: retry */ }
    if (i < retries) await sleep(wait != null ? wait : st.backoff * 2 ** i * (0.5 + Math.random() / 2));
  }
  warnOnce("gave up sending to " + p);
  return "drop";
}

async function drain() {
  if (st.draining || st.disabled) return;
  st.draining = true;
  try {
    while (st.queue.length && !st.disabled) {
      const first = st.queue[0];
      const batch = [];
      for (let i = 0; i < st.queue.length && batch.length < st.batch; ) {
        if (st.queue[i].p === first.p) batch.push(st.queue.splice(i, 1)[0].v); else i++;
      }
      await postIngest(first.p, { [first.p === P_EVENTS ? "events" : "customers"]: batch });
    }
    if (st.disabled) st.queue.length = 0;
  } catch (e) { warnOnce("drain error: " + (e && e.message)); } finally { st.draining = false; }
}

function enqueue(p, v) {
  if (!st.ingestKey || st.disabled) return;
  if (st.queue.length >= MAX_QUEUE) { st.dropped++; return; }
  st.queue.push({ p, v });
  if (st.queue.length >= st.batch && !st.draining) setImmediate(() => { void drain(); });
}

/** Deliver everything queued. Resolves true when empty. */
async function flush(timeoutMs = 5000) {
  const end = Date.now() + timeoutMs;
  if (st.queue.length && !st.draining) void drain();
  while ((st.queue.length || st.draining) && !st.disabled && Date.now() < end) await sleep(10);
  return st.disabled || (st.queue.length === 0 && !st.draining);
}
async function close(timeoutMs = 5000) {
  const ok = await flush(timeoutMs);
  if (st.timer) { clearInterval(st.timer); st.timer = null; }
  return ok;
}

// ------------------------------------------------------------------ tracking
function cleanProps(props) {
  if (!props || typeof props !== "object") return undefined;
  let s; try { s = JSON.stringify(props); } catch (_) { return undefined; }
  if (s.length > 2048) { warnOnce("props over 2 KB dropped"); return undefined; }
  return JSON.parse(s);
}
const iso = (ts) => (ts == null ? new Date().toISOString() : new Date(ts).toISOString());

function makeEvent(name, o) {
  o = o || {};
  const kind = KINDS.has(o.kind) ? o.kind : "custom";
  const ev = { ts: iso(o.ts), name: clip(name, 80), kind };
  const uid = uidOf(o.uid);
  if (uid) ev.uid = uid;
  if (o.feature) ev.feature = clip(o.feature, 80);
  if (o.amount !== undefined && o.amount !== null && Number.isFinite(Number(o.amount))) ev.amount = Number(o.amount);
  if (o.currency) ev.currency = clip(o.currency, 3).toUpperCase();
  const props = cleanProps(o.props);
  if (props) ev.props = props;
  return ev;
}
const strip = (o) => { for (const k of Object.keys(o)) if (o[k] === undefined || o[k] === null || o[k] === "") delete o[k]; return o; };

const track = safe(function track(name, o) { enqueue(P_EVENTS, makeEvent(name, o)); });
const feature = safe(function feature(name, uid) { enqueue(P_EVENTS, makeEvent(name, { kind: "feature", feature: name, uid })); });
const identify = safe(function identify(uid, traits) {
  const t = { ...(traits || {}) };
  const c = strip({ uid: uidOf(uid), signup_at: t.signup_at || t.signupAt, plan: t.plan, mrr: t.mrr, country: t.country, channel: t.channel });
  for (const k of ["signup_at", "signupAt", "plan", "mrr", "country", "channel"]) delete t[k];
  const keys = Object.keys(t).slice(0, 20);
  if (keys.length) { const tr = {}; for (const k of keys) tr[k] = t[k]; const j = cleanProps(tr); if (j) c.traits = j; }
  if (c.uid) enqueue(P_IDENTIFY, c);
});
const signup = safe(function signup(uid, o) {
  o = o || {};
  enqueue(P_EVENTS, makeEvent("signup", { kind: "signup", uid, ts: o.ts, props: strip({ plan: o.plan, channel: o.channel, country: o.country }) }));
  identify(uid, { signup_at: iso(o.ts), plan: o.plan, channel: o.channel, country: o.country });
});
const payment = safe(function payment(uid, amount, currency, o) {
  if (currency && typeof currency === "object") { o = currency; currency = undefined; }
  o = o || {};
  const a = Number(amount);
  enqueue(P_EVENTS, makeEvent(a < 0 ? "refund" : "payment", { kind: a < 0 ? "refund" : "payment", uid, amount: a, currency: currency || o.currency || "USD", ts: o.ts, props: strip({ plan: o.plan }) }));
});
const subscription = safe(function subscription(uid, o) {
  o = o || {};
  enqueue(P_EVENTS, makeEvent("subscription", { kind: "subscription", uid, ts: o.ts, props: strip({ plan: o.plan, mrr: o.mrr === undefined ? undefined : Number(o.mrr), status: o.status || "active" }) }));
  identify(uid, { plan: o.plan, mrr: o.mrr });
});
const churn = safe(function churn(uid, reason) {
  enqueue(P_EVENTS, makeEvent("churn", { kind: "churn", uid, props: reason ? { reason: clip(reason, 200) } : undefined }));
});

// ------------------------------------------------------------------ request middleware
const ID_SEG = /^(\d+|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{16,}|(?=.*\d)[A-Za-z0-9_-]{20,})$/i;
/** "/users/42/orders/9f8..." -> "/users/:id/orders/:id" (no query string, ever). */
function normalizePath(p) {
  const clean = String(p || "/").split("?")[0].split("#")[0];
  const out = clean.split("/").map((s) => (s && ID_SEG.test(s) ? ":id" : s)).join("/");
  return out || "/";
}
function recordRequest(route, method, status, ms, uid) {
  enqueue(P_EVENTS, makeEvent("request", { kind: "request", uid, props: { route: clip(route, 200), method: clip(method, 10), status: Number(status), duration_ms: Math.round(ms * 10) / 10 } }));
}
/** Express/Connect: app.use(prophecy.middleware({ ignorePaths: ["/health"], uid: (req) => req.user && req.user.id })). */
function middleware(o) {
  o = o || {};
  const ignore = o.ignorePaths || [];
  return function prophecyMiddleware(req, res, next) {
    const t0 = process.hrtime.bigint();
    try {
      res.on("finish", () => {
        try {
          const raw = String(req.originalUrl || req.url || "/").split("?")[0];
          if (ignore.some((x) => raw.startsWith(x))) return;
          const route = req.route && typeof req.route.path === "string" ? (req.baseUrl || "") + req.route.path : normalizePath(raw);
          let uid; if (typeof o.uid === "function") { try { uid = o.uid(req); } catch (_) { /* ignore */ } }
          recordRequest(route || "/", req.method, res.statusCode, Number(process.hrtime.bigint() - t0) / 1e6, uid);
        } catch (_) { /* never break a response */ }
      });
    } catch (_) { /* ignore */ }
    next();
  };
}
/** Next.js route handler: export const GET = prophecy.nextMiddleware(async (req, ctx) => Response.json(...), { route: "/api/users/:id" }). */
function nextMiddleware(handler, o) {
  o = o || {};
  return async function prophecyNext(req, ctx) {
    const t0 = process.hrtime.bigint();
    let status = 500;
    try {
      const res = await handler(req, ctx);
      status = res && typeof res.status === "number" ? res.status : 200;
      return res;
    } finally {
      try {
        let route = o.route;
        if (!route) { let p = "/"; try { p = new URL(req.url, "http://x").pathname; } catch (_) { /* ignore */ } route = normalizePath(p); }
        recordRequest(route, req.method || "GET", status, Number(process.hrtime.bigint() - t0) / 1e6);
      } catch (_) { /* ignore */ }
    }
  };
}

// ------------------------------------------------------------------ scan + manifest
function scan(rootDir, o) { return require("./prophecy-scan.cjs").scan(rootDir || process.cwd(), o); }
/** Trim the largest lists until the manifest fits the 512 KB server cap. */
function fitManifest(m) {
  const out = { ...m };
  for (let i = 0; i < 40 && JSON.stringify({ manifest: out }).length > MANIFEST_BYTES; i++) {
    const k = ["routes", "dependencies", "features", "env_names"].sort((a, b) => JSON.stringify(out[b] || []).length - JSON.stringify(out[a] || []).length)[0];
    out[k] = (out[k] || []).slice(0, Math.floor((out[k] || []).length / 2));
  }
  return out;
}
/** Send the manifest with the ingest key. Resolves true on success, false otherwise (never rejects). */
async function sendManifest(manifest) {
  try {
    if (!st.ingestKey) { warnOnce("sendManifest needs init({ ingestKey })"); return false; }
    return (await postIngest(P_MANIFEST, { manifest: fitManifest(manifest) })) === "ok";
  } catch (e) { warnOnce("sendManifest failed: " + (e && e.message)); return false; }
}

// ------------------------------------------------------------------ secret-key API (server side only)
function needSecret() {
  if (inBrowser()) throw new Error("Prophecy secret-key calls must never run in a browser: call them from your server.");
  if (!st.secretKey) throw new Error("Prophecy secretKey missing: init({ secretKey }) or set LINEARJEPH_SECRET_KEY.");
}
async function api(method, p, body) {
  needSecret();
  const retries = st.retries;
  let last;
  for (let i = 0; i <= retries; i++) {
    let wait = null;
    try {
      const res = await fetch(url(p), { method, signal: AbortSignal.timeout(Math.max(st.timeoutMs, 30000)),
        headers: { Authorization: "Bearer " + st.secretKey, "Content-Type": "application/json", "User-Agent": "linearjeph-node/" + SDK_VERSION },
        body: body === undefined ? undefined : JSON.stringify(body) });
      const text = await res.text();
      let json = null; try { json = text ? JSON.parse(text) : null; } catch (_) { /* not json */ }
      if (res.ok) return json;
      if ([429, 500, 502, 503, 504].includes(res.status)) {
        last = new Error("Prophecy " + p + " answered " + res.status);
        const ra = Number(res.headers.get("retry-after"));
        if (res.status === 429 && ra > 0) wait = Math.min(ra, 60) * 1000;
      } else {
        const e = new Error("Prophecy " + p + " answered " + res.status + (json && (json.detail || json.error) ? ": " + (json.detail || json.error) : ""));
        e.status = res.status; throw e;
      }
    } catch (e) { if (e.status) throw e; last = e; }
    if (i < retries) await sleep(wait != null ? wait : st.backoff * 2 ** i * (0.5 + Math.random() / 2));
  }
  throw last || new Error("Prophecy request failed");
}
function importRow(kind, r) {
  const row = { ...r };
  if (row.uid !== undefined && row.uid !== null && row.uid !== "") row.uid = uidOf(row.uid);
  for (const k of ["amount", "mrr"]) if (typeof row[k] === "string" && row[k].trim() !== "" && Number.isFinite(Number(row[k]))) row[k] = Number(row[k]);
  for (const k of Object.keys(row)) if (row[k] === "" || row[k] === undefined) delete row[k];
  if (kind === "events" && typeof row.props === "string") { try { row.props = JSON.parse(row.props); } catch (_) { delete row.props; } }
  return row;
}
/** Backfill history. rows: events {ts,uid,name,kind,feature,amount,currency,props,id} or customers {uid,signup_at,plan,mrr,country,channel}. */
async function importHistory(kind, rows, o) {
  needSecret();
  if (kind !== "events" && kind !== "customers") throw new Error('importHistory kind must be "events" or "customers"');
  if (!Array.isArray(rows)) throw new Error("importHistory rows must be an array");
  o = o || {};
  const total = rows.length, res = { accepted: 0, rejected: 0, errors: [], jobs: [] };
  let i = 0;
  while (i < total) {
    let n = Math.min(IMPORT_BATCH, total - i);
    let chunk = rows.slice(i, i + n).map((r) => importRow(kind, r));
    while (n > 1 && JSON.stringify({ kind, rows: chunk }).length > IMPORT_BYTES) { n = Math.ceil(n / 2); chunk = chunk.slice(0, n); }
    const r = await api("POST", A_IMPORT, { kind, rows: chunk });
    res.accepted += (r && r.accepted) || 0; res.rejected += (r && r.rejected) || 0;
    if (r && r.job !== undefined) res.jobs.push(r.job);
    for (const e of (r && r.errors) || []) if (res.errors.length < 20) res.errors.push(e);
    i += n;
    if (typeof o.onProgress === "function") { try { o.onProgress({ done: i, total, accepted: res.accepted, rejected: res.rejected }); } catch (_) { /* ignore */ } }
  }
  return res;
}
const report = (section) => api("GET", A_REPORT + (section ? "?section=" + encodeURIComponent(section) : ""));
const refresh = () => api("POST", A_REFRESH, {});
const live = () => api("GET", A_LIVE);
/** Signed URL for an iframe in your own admin panel. Returns the URL string. */
async function embedUrl(ttl = 3600) { const r = await api("POST", A_EMBED, { ttl: Math.max(60, Math.min(Number(ttl) || 3600, 86400)) }); return r && r.url; }

module.exports = { SDK_VERSION, init, track, feature, signup, payment, subscription, churn, identify, middleware, nextMiddleware, scan, sendManifest,
  importHistory, report, refresh, live, embedUrl, flush, close, normalizePath, _state: st };
