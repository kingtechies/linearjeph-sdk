"""LINEARJEPH Python SDK core. Standard library only.

Design rules: every public function swallows its own failures (the SDK must never crash the host app), sending happens
on one background daemon thread with a bounded queue, and everything pending is flushed at interpreter exit.
"""
import atexit
import contextlib
import functools
import json
import logging
import os
import queue
import random
import re
import sys
import threading
import time
import traceback
import urllib.error
import urllib.request
from datetime import datetime, timezone

__all__ = ["init", "close", "flush", "capture_exception", "capture_message", "capture_transaction", "start_transaction",
           "transaction", "heartbeat", "monitor", "report_packages", "set_tag", "set_user", "scrub",
           "serialize_exception", "Options", "SDK_VERSION", "DEFAULT_ENDPOINT"]

SDK_VERSION = "1.0.0"
DEFAULT_ENDPOINT = "https://linearjeph.cc"
EVENTS_PATH = "/ingest/v1/events"
HEARTBEAT_PATH = "/ingest/v1/heartbeat"
PACKAGES_PATH = "/ingest/v1/packages"

# Server limits (apps/api-core/errortrack, monitoring, audits).
MAX_FRAMES = 50
MAX_TAGS = 20
MAX_BODY_BYTES = 256 * 1024
BATCH_BYTES = 200 * 1024
MAX_EVENTS_PER_REQUEST = 100
HEARTBEAT_NAME_RE = re.compile(r"^[A-Za-z0-9._-]{1,64}$")
PKG_NAME_RE = re.compile(r"^[\w@./+~:-]{1,200}$")
PKG_VER_RE = re.compile(r"^[\w.+~!:-]{1,100}$")
FILTERED = "[Filtered]"

log = logging.getLogger("linearjeph")

_SENSITIVE_KEY = re.compile(
    r"(pass(word|wd)?|secret|token|authorization|auth|cookie|api[_-]?key|apikey|x-lj-key|private[_-]?key|credential|session|csrf|bearer)",
    re.I)
_SENSITIVE_TEXT = re.compile(
    r"(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|apikey|authorization|access[_-]?key)\b(\s*[=:]\s*)(?:bearer\s+)?[^\s,;&'\"]+")
_BEARER = re.compile(r"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}")


def _rate(v):
    try:
        return max(0.0, min(1.0, float(v)))
    except (TypeError, ValueError):
        return 1.0


class Options(object):
    """Resolved configuration. See docs/sdk/python.md for every field."""

    def __init__(self, key, release=None, environment="production", endpoint=DEFAULT_ENDPOINT, sample_rate=1.0,
                 traces_sample_rate=1.0, before_send=None, max_queue=100, batch_size=20, retries=4, backoff=0.5,
                 timeout=10.0, debug=False, scrub_keys=(), in_app_exclude=None, hooks=True, flush_timeout=5.0):
        self.key = key
        self.release = release
        self.environment = environment or "production"
        self.endpoint = (endpoint or DEFAULT_ENDPOINT).rstrip("/")
        self.sample_rate = _rate(sample_rate)
        self.traces_sample_rate = _rate(traces_sample_rate)
        self.before_send = before_send
        self.max_queue = max(1, int(max_queue))
        self.batch_size = max(1, min(int(batch_size), MAX_EVENTS_PER_REQUEST))
        self.retries = max(0, int(retries))
        self.backoff = float(backoff)
        self.timeout = float(timeout)
        self.debug = bool(debug)
        self.scrub_keys = tuple(str(k).lower() for k in (scrub_keys or ()))
        self.in_app_exclude = tuple(in_app_exclude) if in_app_exclude is not None else (
            "site-packages", "dist-packages", "/lib/python", "\\lib\\python")
        self.hooks = bool(hooks)
        self.flush_timeout = float(flush_timeout)


_lock = threading.RLock()
_state = {"opts": None, "sender": None, "tags": {}, "user": {}, "release": None, "environment": "production",
          "key": None, "endpoint": DEFAULT_ENDPOINT,
          "in_app_exclude": ("site-packages", "dist-packages", "/lib/python", "\\lib\\python")}
_hooked = False
_atexit_registered = False


def _now():
    return datetime.now(timezone.utc).isoformat()


def _safe(fn):
    """Public API wrapper: log (debug) and swallow anything, so the SDK can never break the host application."""
    @functools.wraps(fn)
    def wrapper(*a, **kw):
        try:
            return fn(*a, **kw)
        except Exception:  # noqa: BLE001 - by design
            log.debug("linearjeph: %s failed", fn.__name__, exc_info=True)
            return None
    return wrapper


# ------------------------------------------------------------------ scrubbing

def _is_sensitive(key):
    k = str(key)
    if _SENSITIVE_KEY.search(k):
        return True
    opts = _state["opts"]
    return bool(opts and any(x in k.lower() for x in opts.scrub_keys))


def scrub(data, _depth=0):
    """Return a copy of data (dict/list/str) with values under sensitive keys replaced by "[Filtered]".

    Sensitive keys: password, secret, token, authorization, auth, cookie, api_key, session, csrf, bearer, credential,
    private_key, x-lj-key, plus any `scrub_keys` passed to init(). Strings also lose `password=...` / `Bearer ...` text.
    """
    if _depth > 8:
        return FILTERED
    if isinstance(data, dict):
        return {k: (FILTERED if _is_sensitive(k) else scrub(v, _depth + 1)) for k, v in data.items()}
    if isinstance(data, (list, tuple)):
        return [scrub(v, _depth + 1) for v in data]
    if isinstance(data, str):
        return _BEARER.sub("Bearer " + FILTERED, _SENSITIVE_TEXT.sub(lambda m: m.group(1) + m.group(2) + FILTERED, data))
    return data


def _s(v, n):
    return v[:n] if isinstance(v, str) else ("" if v is None else str(v)[:n])


def _clean_tags(tags):
    out = {}
    for k, v in list((tags or {}).items())[:MAX_TAGS]:
        k = _s(k, 50)
        out[k] = FILTERED if _is_sensitive(k) else _s(scrub(v if isinstance(v, str) else str(v)), 200)
    return out


# ------------------------------------------------------------------ event building

def _in_app(filename):
    return not filename.startswith("<") and not any(p in filename for p in _state["in_app_exclude"])


def serialize_exception(exc_type, exc, tb, release=None, environment=None):
    """Build one contract `error` event (stack oldest -> newest, as the ingest expects)."""
    frames = []
    for fs in traceback.extract_tb(tb)[-MAX_FRAMES:]:
        frames.append({"filename": fs.filename, "function": fs.name, "lineno": fs.lineno or 0,
                       "in_app": _in_app(fs.filename)})
    return {"type": "error", "timestamp": _now(),
            "release": release if release is not None else (_state["release"] or ""),
            "environment": environment or _state["environment"],
            "exception": {"type": getattr(exc_type, "__name__", str(exc_type)),
                          "value": _s(scrub(str(exc)), 500), "stacktrace": frames},
            "tags": {}, "user": {}}


def _finish(ev, tags, rate):
    """Sampling, scope tags, scrubbing, before_send. Returns the event or None."""
    opts = _state["opts"]
    if opts is None or not opts.key:
        return None
    if rate < 1.0 and random.random() >= rate:
        return None
    merged = dict(_state["tags"])
    merged.update(tags or {})
    merged.update(ev.get("tags") or {})
    ev["tags"] = _clean_tags(merged)
    uid = _state["user"].get("id")
    ev["user"] = {"id": _s(uid, 100)} if uid else {}
    if opts.before_send is not None:
        try:
            ev = opts.before_send(ev)
        except Exception:  # noqa: BLE001 - a broken hook must not leak unfiltered data
            log.warning("linearjeph: before_send raised; event dropped", exc_info=opts.debug)
            return None
        if not isinstance(ev, dict):
            return None
        ev["tags"] = _clean_tags(ev.get("tags"))
    return ev


def _emit(ev, tags=None, rate=None):
    opts = _state["opts"]
    sender = _state["sender"]
    if opts is None or sender is None:
        return
    ev = _finish(ev, tags, opts.sample_rate if rate is None else rate)
    if ev is not None:
        sender.put(EVENTS_PATH, ev)


# ------------------------------------------------------------------ sender

class Sender(object):
    """Bounded queue + one background daemon thread. Batches events, retries with backoff, honours Retry-After.

    401 disables sending (the key is wrong or rotated); 400/403/404 drop the item; 413 splits a batch or drops a single
    item; 429, 5xx and network errors retry with exponential backoff and jitter.
    """

    def __init__(self, opts):
        self.opts = opts
        self.q = queue.Queue(maxsize=opts.max_queue)
        self.dropped = 0
        self.disabled = False
        self._stop = threading.Event()
        self._thread = None
        self._pid = None
        self._tlock = threading.Lock()

    def _ensure_thread(self):
        pid = os.getpid()
        if self._thread is not None and self._thread.is_alive() and self._pid == pid:
            return
        with self._tlock:
            if self._thread is not None and self._thread.is_alive() and self._pid == pid:
                return
            if self._pid is not None and self._pid != pid:  # forked worker (gunicorn, multiprocessing): fresh queue
                self.q = queue.Queue(maxsize=self.opts.max_queue)
            self._pid = pid
            self._thread = threading.Thread(target=self._run, args=(self.q,), name="linearjeph-sender", daemon=True)
            self._thread.start()

    def put(self, path, body):
        if self.disabled or self._stop.is_set():
            return False
        try:
            self._ensure_thread()
            self.q.put_nowait((path, body))
            return True
        except queue.Full:
            self.dropped += 1  # never block, never grow without bound inside the customer's process
            return False

    # -- transport
    def _post(self, path, body):
        data = json.dumps(body, default=str, separators=(",", ":")).encode()
        req = urllib.request.Request(self.opts.endpoint + path, data=data, method="POST", headers={
            "Content-Type": "application/json", "X-LJ-Key": self.opts.key,
            "User-Agent": "linearjeph-python/" + SDK_VERSION})
        with urllib.request.urlopen(req, timeout=self.opts.timeout) as r:  # noqa: S310 - endpoint is operator-set
            r.read()
            return r.status

    def _send(self, path, body):
        """True when finished (delivered or undeliverable); False only for a 413 that may be split."""
        for attempt in range(self.opts.retries + 1):
            delay = None
            try:
                self._post(path, body)
                return True
            except urllib.error.HTTPError as e:
                code = e.code
                if code == 401:
                    self.disabled = True
                    log.warning("linearjeph: ingest key rejected (401); sending disabled")
                    return True
                if code == 413:
                    return not (isinstance(body, dict) and len(body.get("events") or ()) > 1)
                if code not in (429, 500, 502, 503, 504):
                    log.debug("linearjeph: %s -> %s dropped", path, code)
                    return True
                if code == 429:
                    try:
                        delay = min(60.0, float(e.headers.get("Retry-After") or 0)) or None
                    except (TypeError, ValueError):
                        delay = None
            except Exception:  # noqa: BLE001 - network error, retry
                pass
            if attempt < self.opts.retries:
                if delay is None:
                    delay = self.opts.backoff * (2 ** attempt) * (0.5 + random.random() / 2)
                if self._stop.wait(delay):
                    return True
        log.debug("linearjeph: gave up sending to %s", path)
        return True

    def _send_events(self, events):
        if self.disabled:
            return
        if not self._send(EVENTS_PATH, {"events": events}):  # 413 on a multi-event batch: split
            mid = len(events) // 2
            self._send_events(events[:mid])
            self._send_events(events[mid:])

    # -- worker
    def _run(self, q):
        while not self._stop.is_set() or not q.empty():
            try:
                path, body = q.get(timeout=0.2)
            except queue.Empty:
                continue
            taken = 1
            try:
                if path == EVENTS_PATH:
                    events, size = [body], len(json.dumps(body, default=str))
                    while len(events) < self.opts.batch_size and size < BATCH_BYTES:
                        try:
                            p2, b2 = q.get_nowait()
                        except queue.Empty:
                            break
                        taken += 1
                        if p2 != EVENTS_PATH:
                            if not self.disabled:
                                self._send(p2, b2)
                            continue
                        events.append(b2)
                        size += len(json.dumps(b2, default=str))
                    self._send_events(events)
                elif not self.disabled:
                    self._send(path, body)
            except Exception:  # noqa: BLE001
                log.debug("linearjeph: sender error", exc_info=True)
            finally:
                for _ in range(taken):
                    q.task_done()

    def flush(self, timeout=5.0):
        end = time.time() + timeout
        while self.q.unfinished_tasks and time.time() < end and not self.disabled:
            time.sleep(0.02)
        return self.disabled or not self.q.unfinished_tasks

    def close(self, timeout=5.0):
        ok = self.flush(timeout)
        self._stop.set()
        return ok


# ------------------------------------------------------------------ public API

def init(key, release=None, environment="production", endpoint=DEFAULT_ENDPOINT, **options):
    """Configure the SDK. Safe to call more than once (the last call wins). Never raises; returns True when enabled.

    Options: sample_rate, traces_sample_rate, before_send, max_queue, batch_size, retries, backoff, timeout, debug,
    scrub_keys, in_app_exclude, hooks, flush_timeout. `hooks=False` skips installing sys/threading excepthooks.
    """
    global _atexit_registered
    try:
        if not key or not isinstance(key, str):
            log.warning("linearjeph: init() without an ingest key; the SDK stays disabled")
            return False
        opts = Options(key, release=release, environment=environment, endpoint=endpoint, **options)
        with _lock:
            old = _state["sender"]
            if old is not None:
                old.close(1.0)
            _state.update(opts=opts, key=key, endpoint=opts.endpoint, release=release, environment=opts.environment,
                          in_app_exclude=opts.in_app_exclude, sender=Sender(opts))
            if opts.debug:
                logging.basicConfig()
                log.setLevel(logging.DEBUG)
            if opts.hooks:
                _install_hooks()
            if not _atexit_registered:
                _atexit_registered = True
                atexit.register(_at_exit)
        return True
    except Exception:  # noqa: BLE001
        log.debug("linearjeph: init failed", exc_info=True)
        return False


def _at_exit():
    s, opts = _state["sender"], _state["opts"]
    if s is not None:
        try:
            s.close(opts.flush_timeout if opts else 5.0)
        except Exception:  # noqa: BLE001
            pass


@_safe
def close(timeout=5.0):
    """Flush and stop the sender. Calling init() again starts a new one."""
    s = _state["sender"]
    if s is None:
        return True
    ok = s.close(timeout)
    _state["sender"] = None
    _state["opts"] = None
    return ok


@_safe
def flush(timeout=5.0):
    """Block up to timeout seconds until the queue is delivered. True when empty."""
    s = _state["sender"]
    return s.flush(timeout) if s else True


@_safe
def set_tag(key, value):
    """Attach a tag to every later event (max 20 tags; key 50 chars, value 200)."""
    _state["tags"][str(key)] = value


@_safe
def set_user(user_id):
    """Attach a user id (a string you choose) to later events. None clears it."""
    _state["user"] = {"id": str(user_id)} if user_id not in (None, "") else {}


@_safe
def capture_exception(exc=None, tags=None):
    """Report an exception (default: the one being handled). Fire-and-forget."""
    if exc is None or exc is True:
        t, v, tb = sys.exc_info()
    elif isinstance(exc, tuple) and len(exc) == 3:
        t, v, tb = exc
    else:
        t, v, tb = type(exc), exc, getattr(exc, "__traceback__", None)
    if t is None:
        return
    _emit(serialize_exception(t, v, tb), tags)


@_safe
def capture_message(message, level="info", tags=None):
    """Report a message. The ingest stores it as an error event of type "Message" (grouped by its text)."""
    ev = {"type": "error", "timestamp": _now(), "release": _state["release"] or "", "environment": _state["environment"],
          "exception": {"type": "Message", "value": _s(scrub(str(message)), 500), "stacktrace": []},
          "tags": {"level": str(level)}}
    _emit(ev, tags)


@_safe
def capture_transaction(name, duration_ms, status="ok", tags=None):
    """Report one timed operation (counts towards p50/p95/throughput). Needs a plan with performance monitoring."""
    ev = {"type": "transaction", "timestamp": _now(), "release": _state["release"] or "",
          "environment": _state["environment"],
          "transaction": {"name": _s(name, 200), "duration_ms": float(duration_ms), "status": _s(status, 20) or "ok"},
          "tags": {}}
    opts = _state["opts"]
    _emit(ev, tags, opts.traces_sample_rate if opts else 1.0)


class start_transaction(contextlib.ContextDecorator):
    """Time a block or function: `with start_transaction("checkout"):` or `@start_transaction("job")`.

    Status becomes "error" if the block raises. The exception is neither swallowed nor captured.
    """

    def __init__(self, name):
        self.name = name
        self.status = "ok"
        self._t0 = None

    def __enter__(self):
        self._t0 = time.perf_counter()
        return self

    def __exit__(self, et, ev, tb):
        try:
            if et is not None:
                self.status = "error"
            capture_transaction(self.name, (time.perf_counter() - self._t0) * 1000.0, self.status)
        except Exception:  # noqa: BLE001
            pass
        return False

    def _recreate_cm(self):  # a fresh timer per call when used as a decorator
        return type(self)(self.name)


transaction = start_transaction


@_safe
def heartbeat(name, status="ok", interval_s=None):
    """Check in for a cron job / worker. `interval_s` (30 .. 2592000) tells the monitor how often to expect you."""
    if not HEARTBEAT_NAME_RE.match(str(name)):
        log.warning("linearjeph: heartbeat name must match [A-Za-z0-9._-]{1,64}; ignored")
        return
    if status not in ("ok", "error"):
        status = "error"
    body = {"name": str(name), "status": status}
    if interval_s:
        body["interval_s"] = max(30, min(int(interval_s), 30 * 86400))
    s = _state["sender"]
    if s:
        s.put(HEARTBEAT_PATH, body)


class monitor(contextlib.ContextDecorator):
    """Cron/worker wrapper: heartbeat "ok" on success, "error" if the block raises (then re-raises).

        @linearjeph.monitor("nightly-report", interval_s=86400)
        def run(): ...

        with linearjeph.monitor("sync", interval_s=900):
            ...

    Pass `capture=True` to also report the exception as an error event.
    """

    def __init__(self, name, interval_s=None, capture=False):
        self.name, self.interval_s, self.capture = name, interval_s, capture

    def __enter__(self):
        return self

    def __exit__(self, et, ev, tb):
        try:
            if et is not None and self.capture and not issubclass(et, (SystemExit, KeyboardInterrupt)):
                capture_exception((et, ev, tb))
            heartbeat(self.name, "ok" if et is None else "error", self.interval_s)
        except Exception:  # noqa: BLE001
            pass
        return False


def _distributions():
    from importlib import metadata
    pk = {}
    for d in metadata.distributions():
        n = d.metadata["Name"]
        if n and PKG_NAME_RE.match(n) and PKG_VER_RE.match(d.version or ""):
            pk[n.lower()] = d.version
    return pk


@_safe
def report_packages():
    """Send installed distributions (PyPI) for the dependency audit. Replaces the previous SDK-reported list.

    Returns the number of packages queued (0 when the SDK is not initialised)."""
    s = _state["sender"]
    if not s:
        return 0
    out, size = [], 64
    for n, v in sorted(_distributions().items()):
        # the server replaces the whole list per request, so one request must hold everything: cap by size
        add = len(n) + len(v) + 30
        if len(out) >= 5000 or size + add > BATCH_BYTES:
            break
        out.append({"name": n, "version": v})
        size += add
    s.put(PACKAGES_PATH, {"ecosystem": "PyPI", "packages": out})
    return len(out)


# ------------------------------------------------------------------ automatic capture

def _install_hooks():
    global _hooked
    if _hooked:
        return
    _hooked = True
    prev = sys.excepthook

    def hook(t, v, tb):
        try:
            if not issubclass(t, KeyboardInterrupt):
                _emit(serialize_exception(t, v, tb))
                flush(2.0)
        except Exception:  # noqa: BLE001
            pass
        prev(t, v, tb)

    sys.excepthook = hook
    prev_t = threading.excepthook

    def thook(args):
        try:
            if args.exc_type is not SystemExit:
                _emit(serialize_exception(args.exc_type, args.exc_value, args.exc_traceback))
        except Exception:  # noqa: BLE001
            pass
        prev_t(args)

    threading.excepthook = thook
