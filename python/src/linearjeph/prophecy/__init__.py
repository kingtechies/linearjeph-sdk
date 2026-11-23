"""LINEARJEPH Prophecy: growth-intelligence tracking, codebase scan, history import and report access.

Standard library only. Tracking calls never raise into the host app (failures are swallowed and logged once), events go
through a bounded in-memory queue (10,000) to one background daemon thread, batched every 2 s or 100 events, gzip
compressed, retried with backoff on 429/5xx and flushed at interpreter exit. Secret-key calls are server side only.
"""
import atexit
import collections
import functools
import gzip
import hashlib
import json
import logging
import os
import random
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone

from .scanner import scan as _scan

__all__ = ["init", "track", "feature", "signup", "payment", "subscription", "churn", "identify", "scan", "send_manifest",
           "import_history", "report", "refresh", "live", "embed_url", "flush", "close", "normalize_path", "ProphecyError",
           "SDK_VERSION"]

SDK_VERSION = "1.3.0"
DEFAULT_ENDPOINT = "https://linearjeph.cc"
P_EVENTS = "/ingest/v1/prophecy/events"
P_IDENTIFY = "/ingest/v1/prophecy/identify"
P_MANIFEST = "/ingest/v1/prophecy/manifest"
A_IMPORT = "/api/v1/prophecy/sdk/import"
A_REPORT = "/api/v1/prophecy/sdk/report"
A_REFRESH = "/api/v1/prophecy/sdk/refresh"
A_LIVE = "/api/v1/prophecy/sdk/live"
A_EMBED = "/api/v1/prophecy/sdk/embed"
MAX_QUEUE = 10000
BATCH = 100
INTERVAL = 2.0
IMPORT_BATCH = 5000
IMPORT_BYTES = int(4.5 * 1024 * 1024)
MANIFEST_BYTES = 500 * 1024
KINDS = {"signup", "login", "active", "feature", "payment", "refund", "subscription", "churn", "pageview", "request", "error", "custom"}
RETRY_STATUS = (429, 500, 502, 503, 504)

log = logging.getLogger("linearjeph.prophecy")


class ProphecyError(RuntimeError):
    """Raised by the secret-key calls (import, report, refresh, live, embed_url) when the request cannot be completed."""

    def __init__(self, message, status=None):
        super().__init__(message)
        self.status = status


_cond = threading.Condition()
_st = {"ingest_key": None, "secret_key": None, "endpoint": DEFAULT_ENDPOINT, "hash": True, "queue": collections.deque(), "thread": None,
       "busy": False, "stop": False, "disabled": False, "dropped": 0, "retries": 4, "backoff": 0.5, "timeout": 10.0, "batch": BATCH,
       "interval": INTERVAL, "warned": set(), "atexit": False}


def _warn_once(msg):
    if msg in _st["warned"]:
        return
    _st["warned"].add(msg)
    try:
        log.warning("linearjeph.prophecy: %s", msg)
    except Exception:  # noqa: BLE001
        pass


def _safe(fn):
    """Public tracking wrapper: swallow anything (log once) so the SDK can never break the host application."""
    @functools.wraps(fn)
    def wrapper(*a, **kw):
        try:
            return fn(*a, **kw)
        except Exception as e:  # noqa: BLE001 - by design
            _warn_once("%s failed: %s" % (fn.__name__, e))
            return None
    return wrapper


def _sha256(s):
    return hashlib.sha256(str(s).encode("utf-8")).hexdigest()


def _uid(uid):
    if uid is None or uid == "":
        return ""
    return _sha256(uid) if _st["hash"] else str(uid)[:200]


def _iso(ts=None):
    if ts is None:
        return datetime.now(timezone.utc).isoformat()
    if isinstance(ts, datetime):
        return (ts if ts.tzinfo else ts.replace(tzinfo=timezone.utc)).isoformat()
    if isinstance(ts, (int, float)):
        return datetime.fromtimestamp(ts / 1000.0 if ts > 1e11 else ts, timezone.utc).isoformat()
    return str(ts)


def init(ingest_key=None, secret_key=None, endpoint=None, hash_user_ids=True, batch_size=BATCH, flush_interval=INTERVAL, retries=4,
         backoff=0.5, timeout=10.0, hooks=True):
    """Configure Prophecy. Keys fall back to LINEARJEPH_INGEST_KEY / LINEARJEPH_SECRET_KEY, endpoint to LINEARJEPH_ENDPOINT.
    Returns True when at least one key is set. Never raises."""
    try:
        env = os.environ
        ingest_key = ingest_key or env.get("LINEARJEPH_INGEST_KEY") or None
        secret_key = secret_key or env.get("LINEARJEPH_SECRET_KEY") or None
        if ingest_key and str(ingest_key).startswith("ljp_sk_"):
            log.error("linearjeph.prophecy: ingest_key looks like a secret key (ljp_sk_...); use the application ingest key. Tracking disabled.")
            return False
        with _cond:
            _st["queue"].clear()
            _st.update(ingest_key=str(ingest_key) if ingest_key else None, secret_key=str(secret_key) if secret_key else None,
                       endpoint=str(endpoint or env.get("LINEARJEPH_ENDPOINT") or DEFAULT_ENDPOINT).rstrip("/"),
                       hash=bool(hash_user_ids), stop=False, batch=max(1, min(int(batch_size), 500)), interval=max(0.05, float(flush_interval)),
                       retries=max(0, int(retries)), backoff=float(backoff), timeout=float(timeout), disabled=False, dropped=0)
            _st["warned"].clear()
        if _st["ingest_key"]:
            _ensure_thread()
            if hooks and not _st["atexit"]:
                _st["atexit"] = True
                atexit.register(_at_exit)
        return bool(_st["ingest_key"] or _st["secret_key"])
    except Exception:  # noqa: BLE001
        return False


# ------------------------------------------------------------------ transport
def _post_ingest(path, body, last_chance=False):
    data = gzip.compress(json.dumps(body, separators=(",", ":")).encode("utf-8"))
    retries = 0 if last_chance else _st["retries"]
    for i in range(retries + 1):
        wait = None
        req = urllib.request.Request(_st["endpoint"] + path, data=data, method="POST", headers={
            "Content-Type": "application/json", "Content-Encoding": "gzip", "X-LJ-Key": _st["ingest_key"] or "",
            "User-Agent": "linearjeph-python/" + SDK_VERSION})
        try:
            with urllib.request.urlopen(req, timeout=_st["timeout"]) as r:
                r.read()
                return True
        except urllib.error.HTTPError as e:
            code = e.code
            if code == 401:
                _st["disabled"] = True
                _warn_once("ingest key rejected (401); tracking disabled")
                return False
            if code == 402:
                _warn_once("Prophecy subscription inactive (402); events dropped")
                return False
            if code not in RETRY_STATUS:
                _warn_once("%s answered %s; batch dropped" % (path, code))
                return False
            if code == 429:
                try:
                    ra = float(e.headers.get("Retry-After") or 0)
                    if ra > 0:
                        wait = min(ra, 60.0)
                except (TypeError, ValueError):
                    pass
        except Exception:  # noqa: BLE001 - network error or timeout: retry
            pass
        if i < retries:
            time.sleep(wait if wait is not None else _st["backoff"] * (2 ** i) * (0.5 + random.random() / 2))
    _warn_once("gave up sending to " + path)
    return False


def _take_batch():
    q = _st["queue"]
    if not q:
        return None, []
    path = q[0][0]
    batch, keep = [], collections.deque()
    while q:
        item = q.popleft()
        if item[0] == path and len(batch) < _st["batch"]:
            batch.append(item[1])
        else:
            keep.append(item)
    q.extend(keep)
    return path, batch


def _worker():
    while True:
        with _cond:
            if _st["stop"] and not _st["queue"]:
                return
            if not _st["queue"] and not _st["stop"]:
                _cond.wait(_st["interval"])
            elif _st["queue"] and len(_st["queue"]) < _st["batch"] and not _st["stop"] and not _st.get("flushing"):
                _cond.wait(_st["interval"])
            if _st["disabled"]:
                _st["queue"].clear()
                _cond.notify_all()
                continue
            path, batch = _take_batch()
            if batch:
                _st["busy"] = True
        if batch:
            try:
                _post_ingest(path, {("events" if path == P_EVENTS else "customers"): batch}, last_chance=_st["stop"])
            except Exception as e:  # noqa: BLE001
                _warn_once("send error: %s" % e)
            finally:
                with _cond:
                    _st["busy"] = False
                    _cond.notify_all()


def _ensure_thread():
    with _cond:
        t = _st["thread"]
        if t is not None and t.is_alive():
            return
        _st["stop"] = False
        t = threading.Thread(target=_worker, name="linearjeph-prophecy", daemon=True)
        _st["thread"] = t
        t.start()


def _enqueue(path, item):
    if not _st["ingest_key"] or _st["disabled"]:
        return
    with _cond:
        if len(_st["queue"]) >= MAX_QUEUE:
            _st["dropped"] += 1
            return
        _st["queue"].append((path, item))
        if len(_st["queue"]) >= _st["batch"]:
            _cond.notify_all()
    t = _st["thread"]
    if t is None or not t.is_alive():
        _ensure_thread()


def flush(timeout=5.0):
    """Wait until everything queued is delivered (or timeout seconds). Returns True when empty."""
    end = time.time() + timeout
    with _cond:
        _st["flushing"] = True
        try:
            while (_st["queue"] or _st["busy"]) and not _st["disabled"]:
                left = end - time.time()
                if left <= 0:
                    break
                _cond.notify_all()
                _cond.wait(min(left, 0.05))
            return _st["disabled"] or not (_st["queue"] or _st["busy"])
        finally:
            _st["flushing"] = False


def close(timeout=5.0):
    """Flush, then stop the background thread."""
    ok = flush(timeout)
    with _cond:
        _st["stop"] = True
        _cond.notify_all()
    t = _st["thread"]
    if t is not None and t.is_alive() and t is not threading.current_thread():
        t.join(min(timeout, 2.0))
    return ok


def _at_exit():
    try:
        flush(5.0)
    except Exception:  # noqa: BLE001
        pass


# ------------------------------------------------------------------ tracking
def _props(props):
    if not isinstance(props, dict):
        return None
    try:
        s = json.dumps(props, default=str)
    except (TypeError, ValueError):
        return None
    if len(s) > 2048:
        _warn_once("props over 2 KB dropped")
        return None
    return json.loads(s)


def _event(name, uid=None, kind="custom", feature=None, amount=None, currency=None, props=None, ts=None):
    ev = {"ts": _iso(ts), "name": str(name)[:80], "kind": kind if kind in KINDS else "custom"}
    u = _uid(uid)
    if u:
        ev["uid"] = u
    if feature:
        ev["feature"] = str(feature)[:80]
    if amount is not None:
        ev["amount"] = float(amount)
    if currency:
        ev["currency"] = str(currency)[:3].upper()
    p = _props(props)
    if p:
        ev["props"] = p
    return ev


def _strip(d):
    return {k: v for k, v in d.items() if v is not None and v != ""}


@_safe
def track(name, uid=None, kind="custom", feature=None, amount=None, currency=None, props=None, ts=None):
    """Record one event. kind: signup, login, active, feature, payment, refund, subscription, churn, pageview, request, error, custom."""
    _enqueue(P_EVENTS, _event(name, uid, kind, feature, amount, currency, props, ts))


@_safe
def feature(name, uid=None):
    """Shorthand for a feature-use event (kind=feature)."""
    _enqueue(P_EVENTS, _event(name, uid, "feature", feature=name))


@_safe
def identify(uid, traits=None, **kw):
    """Set customer traits: signup_at, plan, mrr, country, channel; every other key goes to traits (max 20 keys, 2 KB)."""
    t = dict(traits or {})
    t.update(kw)
    c = _strip({"uid": _uid(uid), "signup_at": t.pop("signup_at", None) or t.pop("signupAt", None), "plan": t.pop("plan", None),
                "mrr": t.pop("mrr", None), "country": t.pop("country", None), "channel": t.pop("channel", None)})
    t.pop("signupAt", None)
    extra = _props(dict(list(t.items())[:20]))
    if extra:
        c["traits"] = extra
    if c.get("uid"):
        _enqueue(P_IDENTIFY, c)


@_safe
def signup(uid, plan=None, channel=None, country=None, ts=None):
    _enqueue(P_EVENTS, _event("signup", uid, "signup", props=_strip({"plan": plan, "channel": channel, "country": country}), ts=ts))
    identify(uid, signup_at=_iso(ts), plan=plan, channel=channel, country=country)


@_safe
def payment(uid, amount, currency="USD", plan=None, ts=None):
    """A payment (amount > 0) or a refund (amount < 0)."""
    a = float(amount)
    k = "refund" if a < 0 else "payment"
    _enqueue(P_EVENTS, _event(k, uid, k, amount=a, currency=currency or "USD", props=_strip({"plan": plan}), ts=ts))


@_safe
def subscription(uid, plan, mrr, status="active", ts=None):
    """status: active, cancelled or paused."""
    _enqueue(P_EVENTS, _event("subscription", uid, "subscription", props=_strip({"plan": plan, "mrr": float(mrr), "status": status}), ts=ts))
    identify(uid, plan=plan, mrr=mrr)


@_safe
def churn(uid, reason=None, ts=None):
    _enqueue(P_EVENTS, _event("churn", uid, "churn", props=_strip({"reason": str(reason)[:200] if reason else None}), ts=ts))


# ------------------------------------------------------------------ requests (used by the middlewares)
from .middleware import normalize_path  # noqa: E402  (re-exported)


def record_request(route, method, status, duration_ms, uid=None):
    """Record one request: route template, method, status, duration only. Never bodies, headers or query strings."""
    try:
        _enqueue(P_EVENTS, _event("request", uid, "request", props={"route": str(route)[:200], "method": str(method)[:10],
                                                                    "status": int(status), "duration_ms": round(float(duration_ms), 1)}))
    except Exception:  # noqa: BLE001
        pass


# ------------------------------------------------------------------ scan + manifest
def scan(root_dir=".", max_files=None):
    """Scan root_dir locally and return the manifest dict (names and counts only; see docs/sdk/prophecy.md)."""
    return _scan(root_dir, max_files) if max_files else _scan(root_dir)


def _fit_manifest(m):
    out = dict(m)
    for _ in range(40):
        if len(json.dumps({"manifest": out})) <= MANIFEST_BYTES:
            break
        k = max(("routes", "dependencies", "features", "env_names"), key=lambda x: len(json.dumps(out.get(x) or [])))
        out[k] = (out.get(k) or [])[:len(out.get(k) or []) // 2]
    return out


def send_manifest(manifest):
    """POST the manifest with the ingest key. Returns True on success, False otherwise (never raises)."""
    try:
        if not _st["ingest_key"]:
            _warn_once("send_manifest needs init(ingest_key=...)")
            return False
        return _post_ingest(P_MANIFEST, {"manifest": _fit_manifest(manifest)})
    except Exception as e:  # noqa: BLE001
        _warn_once("send_manifest failed: %s" % e)
        return False


# ------------------------------------------------------------------ secret-key API (server side only)
def _api(method, path, body=None):
    if not _st["secret_key"]:
        raise ProphecyError("Prophecy secret_key missing: init(secret_key=...) or set LINEARJEPH_SECRET_KEY")
    data = json.dumps(body, separators=(",", ":")).encode("utf-8") if body is not None else None
    last = None
    for i in range(_st["retries"] + 1):
        wait = None
        req = urllib.request.Request(_st["endpoint"] + path, data=data, method=method, headers={
            "Authorization": "Bearer " + _st["secret_key"], "Content-Type": "application/json", "User-Agent": "linearjeph-python/" + SDK_VERSION})
        try:
            with urllib.request.urlopen(req, timeout=max(_st["timeout"], 30.0)) as r:
                txt = r.read().decode("utf-8") or "null"
                return json.loads(txt)
        except urllib.error.HTTPError as e:
            if e.code in RETRY_STATUS:
                last = ProphecyError("Prophecy %s answered %s" % (path, e.code), e.code)
                if e.code == 429:
                    try:
                        ra = float(e.headers.get("Retry-After") or 0)
                        wait = min(ra, 60.0) if ra > 0 else None
                    except (TypeError, ValueError):
                        pass
            else:
                detail = ""
                try:
                    j = json.loads(e.read().decode("utf-8"))
                    detail = ": %s" % (j.get("detail") or j.get("error") or "")
                except Exception:  # noqa: BLE001
                    pass
                raise ProphecyError("Prophecy %s answered %s%s" % (path, e.code, detail), e.code)
        except (urllib.error.URLError, OSError) as e:
            last = ProphecyError("Prophecy request failed: %s" % e)
        if i < _st["retries"]:
            time.sleep(wait if wait is not None else _st["backoff"] * (2 ** i) * (0.5 + random.random() / 2))
    raise last or ProphecyError("Prophecy request failed")


def _import_row(kind, r):
    row = dict(r)
    if row.get("uid") not in (None, ""):
        row["uid"] = _uid(row["uid"])
    for k in ("amount", "mrr"):
        if isinstance(row.get(k), str) and row[k].strip() != "":
            try:
                row[k] = float(row[k])
            except ValueError:
                pass
    row = {k: v for k, v in row.items() if v is not None and v != ""}
    if kind == "events" and isinstance(row.get("props"), str):
        try:
            row["props"] = json.loads(row["props"])
        except ValueError:
            row.pop("props")
    return row


def import_history(kind, rows, on_progress=None):
    """Backfill history with the secret key. events rows: ts, uid, name, kind, feature, amount, currency, props, id.
    customers rows: uid, signup_at, plan, mrr, country, channel. Batches of 5,000, retries on 429/5xx.
    Returns {accepted, rejected, errors (first 20), jobs}."""
    if kind not in ("events", "customers"):
        raise ProphecyError('import_history kind must be "events" or "customers"')
    if not _st["secret_key"]:
        raise ProphecyError("Prophecy secret_key missing: init(secret_key=...) or set LINEARJEPH_SECRET_KEY")
    rows = list(rows)
    total = len(rows)
    res = {"accepted": 0, "rejected": 0, "errors": [], "jobs": []}
    i = 0
    while i < total:
        n = min(IMPORT_BATCH, total - i)
        chunk = [_import_row(kind, r) for r in rows[i:i + n]]
        while n > 1 and len(json.dumps({"kind": kind, "rows": chunk})) > IMPORT_BYTES:
            n = (n + 1) // 2
            chunk = chunk[:n]
        r = _api("POST", A_IMPORT, {"kind": kind, "rows": chunk}) or {}
        res["accepted"] += int(r.get("accepted") or 0)
        res["rejected"] += int(r.get("rejected") or 0)
        if r.get("job") is not None:
            res["jobs"].append(r["job"])
        for e in r.get("errors") or []:
            if len(res["errors"]) < 20:
                res["errors"].append(e)
        i += n
        if on_progress:
            try:
                on_progress({"done": i, "total": total, "accepted": res["accepted"], "rejected": res["rejected"]})
            except Exception:  # noqa: BLE001
                pass
    return res


def report(section=None):
    """Latest report: {"report": {...}, "created_at", "stale"}; section="revenue" returns one section."""
    from urllib.parse import quote
    return _api("GET", A_REPORT + ("?section=" + quote(section) if section else ""))


def refresh():
    """Queue a new report (limited per hour)."""
    return _api("POST", A_REFRESH, {})


def live():
    """Realtime counters."""
    return _api("GET", A_LIVE)


def embed_url(ttl=3600):
    """Signed URL to show the report in an iframe in your own admin panel. Returns the URL string."""
    r = _api("POST", A_EMBED, {"ttl": max(60, min(int(ttl or 3600), 86400))})
    return r.get("url") if isinstance(r, dict) else None
