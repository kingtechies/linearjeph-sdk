"""Metrics, structured logs, trace spans, cron check-ins and release/deploy markers.

Same rules as the core: nothing here raises into the host app, nothing blocks (everything goes onto the bounded sender
queue), and personal data is scrubbed on the client before anything leaves the process.
"""
import contextlib
import contextvars
import functools
import logging
import math
import os
import random
import re
import threading
import time
import uuid
from datetime import datetime, timezone

from . import client
from .client import (CHECKIN_PATH, DEPLOYS_PATH, LOGS_PATH, METRICS_PATH, RELEASES_PATH, SPANS_PATH, _safe, _state)

log = logging.getLogger("linearjeph")

NAME_RE = re.compile(r"^[A-Za-z][A-Za-z0-9_.:/-]{0,99}$")
MAX_TAGS = 10
RESERVOIR = 100
LEVELS = {"debug": 10, "info": 20, "warning": 30, "warn": 30, "error": 40, "critical": 50, "fatal": 50}

# ------------------------------------------------------------------ PII scrubbing (emails, cards, tokens)

_EMAIL = re.compile(r"[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63})+")
_CARD = re.compile(r"(?<![\d-])(?:\d[ -]?){12,18}\d(?![\d-])")
_JWT = re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*")
_PREFIXED = re.compile(r"\b(?:lj_[a-z]{2,5}_[A-Za-z0-9_-]{8,}|ljp_[A-Za-z0-9_-]{8,}|ljr_[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,}"
                       r"|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,})\b")


def _luhn(digits):
    total, alt = 0, False
    for ch in reversed(digits):
        d = int(ch)
        if alt:
            d = d * 2 - 9 if d * 2 > 9 else d * 2
        total += d
        alt = not alt
    return total % 10 == 0


def _card(m):
    digits = re.sub(r"\D", "", m.group(0))
    return "[card]" if 13 <= len(digits) <= 19 and _luhn(digits) else m.group(0)


def scrub_pii(text):
    """Replace emails, payment card numbers (Luhn-valid), JWTs, API keys and bearer tokens, plus `password=...` style text."""
    text = client.scrub(str(text))  # password=..., Bearer ...
    text = _JWT.sub("[token]", text)
    text = _PREFIXED.sub("[token]", text)
    text = _EMAIL.sub("[email]", text)
    return _CARD.sub(_card, text)


def _clean(text, n):
    opts = _state["opts"]
    t = str(text)[:n * 2]
    if opts is None or opts.pii_scrub:
        t = scrub_pii(t)
    return t[:n]


def _now_iso():
    return datetime.now(timezone.utc).isoformat()


# ------------------------------------------------------------------ metrics

_mlock = threading.Lock()
_metrics = {}
_metric_drops = [0]


def _mtags(tags):
    out = {}
    for k, v in list((tags or {}).items())[:MAX_TAGS]:
        k = str(k)[:50]
        out[k] = client.FILTERED if client._is_sensitive(k) else _clean(v, 100)
    return out


def _record(typ, name, value, tags):
    opts = _state["opts"]
    if opts is None or _state["sender"] is None:
        return
    if not isinstance(name, str) or not NAME_RE.match(name):
        return
    v = float(value)
    if not math.isfinite(v):
        return
    t = _mtags(tags)
    _state["sender"]._ensure_thread()  # the sender thread also flushes aggregates on its timer
    key = (typ, name, tuple(sorted(t.items())))
    with _mlock:
        a = _metrics.get(key)
        if a is None:
            if len(_metrics) >= opts.max_metric_series:
                _metric_drops[0] += 1  # bounded memory: new series beyond the cap are dropped and counted
                return
            a = _metrics[key] = {"tags": t, "count": 0, "sum": 0.0, "min": v, "max": v, "last": v, "values": []}
        a["count"] += 1
        a["sum"] += v
        a["min"], a["max"], a["last"] = min(a["min"], v), max(a["max"], v), v
        if typ == "histogram":
            if len(a["values"]) < RESERVOIR:
                a["values"].append(v)
            else:
                j = random.randrange(a["count"])
                if j < RESERVOIR:
                    a["values"][j] = v


@_safe
def incr(name, value=1, tags=None):
    """Counter: add `value` (default 1). Sent as one summed point per flush interval."""
    _record("counter", name, value, tags)


@_safe
def gauge(name, value, tags=None):
    """Gauge: the latest value wins within a flush interval."""
    _record("gauge", name, value, tags)


@_safe
def histogram(name, value, tags=None):
    """Histogram: count, sum, min, max and a bounded sample for p50/p95/p99."""
    _record("histogram", name, value, tags)


class timer(contextlib.ContextDecorator):
    """`with linearjeph.timer("db.query", tags={"table": "users"}):` or `@linearjeph.timer("job")`. Records milliseconds."""

    def __init__(self, name, tags=None):
        self.name, self.tags, self._t0 = name, tags, None

    def __enter__(self):
        self._t0 = time.perf_counter()
        return self

    def __exit__(self, et, ev, tb):
        try:
            histogram(self.name, (time.perf_counter() - self._t0) * 1000.0, self.tags)
        except Exception:  # noqa: BLE001
            pass
        return False

    def _recreate_cm(self):
        return type(self)(self.name, self.tags)


def flush_metrics():
    """Move the in-process aggregates onto the sender queue. Called every `metrics_interval` s and by flush()/close()."""
    s = _state["sender"]
    with _mlock:
        if not _metrics or s is None:
            return 0
        snap, _metrics_copy = dict(_metrics), None
        _metrics.clear()
    ts, items = _now_iso(), []
    for (typ, name, _), a in snap.items():
        it = {"name": name, "type": typ, "tags": a["tags"], "timestamp": ts}
        if typ == "counter":
            it["value"] = a["sum"]
        elif typ == "gauge":
            it["value"] = a["last"]
        else:
            it.update(count=a["count"], sum=a["sum"], min=a["min"], max=a["max"], values=a["values"])
        items.append(it)
    for i in range(0, len(items), 200):
        s.put(METRICS_PATH, {"metrics": items[i:i + 200]})
    return len(items)


def dropped_metric_series():
    return _metric_drops[0]


def attach(sender):
    """Called by init(): wire metric flushing into the sender thread and flush()."""
    sender.on_tick = flush_metrics
    sender.pre_flush = flush_metrics
    with _mlock:
        _metrics.clear()


# ------------------------------------------------------------------ structured logs

def _level_no(level):
    if isinstance(level, int):
        return level
    return LEVELS.get(str(level).lower(), 20)


@_safe
def log_event(message, level="info", logger=None, **context):
    """Send one structured log line. `context` becomes searchable fields. Trace ids are added inside a span."""
    opts, s = _state["opts"], _state["sender"]
    if opts is None or s is None:
        return
    no = _level_no(level)
    if no < _level_no(opts.log_level):
        return
    if opts.sample_logs < 1.0 and no < 40 and random.random() >= opts.sample_logs:
        return
    ctx = {}
    for k, v in list(context.items())[:30]:
        k = str(k)[:50]
        ctx[k] = client.FILTERED if client._is_sensitive(k) else (v if isinstance(v, (int, float, bool)) else _clean(v, 300))
    cur = _span_ctx.get()
    item = {"timestamp": _now_iso(), "level": {10: "debug", 20: "info", 30: "warning", 40: "error", 50: "critical"}.get(no // 10 * 10, "info"),
            "message": _clean(message, 4000), "logger": str(logger or "")[:100], "context": ctx,
            "release": _state["release"] or "", "environment": _state["environment"]}
    if cur is not None:
        item["trace_id"], item["span_id"] = cur.trace_id, cur.span_id
    if opts.before_send_log is not None:
        item = opts.before_send_log(item)  # inspect, edit or drop (return None) each log line before it is queued
        if not isinstance(item, dict):
            return
    s.put(LOGS_PATH, item)


class LinearjephHandler(logging.Handler):
    """`logging` handler: `logging.getLogger().addHandler(linearjeph.LinearjephHandler())`. Extra fields become context."""
    _STD = set(logging.LogRecord("", 0, "", 0, "", (), None).__dict__) | {"message", "asctime", "taskName"}

    def emit(self, record):
        try:
            if record.name.split(".")[0] in ("linearjeph", "urllib3"):
                return  # never log our own transport (no feedback loop)
            msg = record.getMessage()
            if record.exc_info and record.exc_info[0] is not None:
                import traceback
                msg += "\n" + "".join(traceback.format_exception(*record.exc_info))[-1500:]
            ctx = {k: v for k, v in record.__dict__.items() if k not in self._STD and not k.startswith("_")}
            log_event(msg, record.levelno, record.name, **ctx)
        except Exception:  # noqa: BLE001
            pass


_handlers = []


def install_logging(level=logging.INFO, logger=None):
    """Attach a LinearjephHandler to `logger` (default: the root logger). Idempotent per logger. Returns the handler."""
    target = logger or logging.getLogger()
    for h, t in _handlers:
        if t is target:
            h.setLevel(level)
            return h
    h = LinearjephHandler(level)
    target.addHandler(h)
    _handlers.append((h, target))
    return h


# ------------------------------------------------------------------ spans and tracing

_span_ctx = contextvars.ContextVar("linearjeph_span", default=None)


class Span(object):
    def __init__(self, name, kind, trace_id, parent_id, sampled, tags):
        self.name, self.kind, self.trace_id, self.parent_id, self.sampled = name, kind, trace_id, parent_id, sampled
        self.span_id = uuid.uuid4().hex[:16]
        self.tags = dict(tags or {})
        self.status = "ok"
        self.start = _now_iso()
        self._t0 = time.perf_counter()
        self._token = None
        self.done = False

    def set_tag(self, key, value):
        self.tags[str(key)] = value

    def traceparent(self):
        return "00-%s-%s-%s" % (self.trace_id.rjust(32, "0"), self.span_id, "01" if self.sampled else "00")


def current_span():
    return _span_ctx.get()


def parse_traceparent(value):
    """W3C `traceparent` header -> (trace_id, parent_span_id, sampled) or None."""
    m = re.match(r"^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$", str(value or "").strip().lower())
    return (m.group(1), m.group(2), bool(int(m.group(3), 16) & 1)) if m else None


def start_span(name, kind="internal", tags=None, traceparent=None):
    """Begin a span and make it current. Pair with finish_span(). Prefer the `span()` context manager."""
    try:
        opts = _state["opts"]
        parent = _span_ctx.get()
        if parent is not None:
            trace, pid, sampled = parent.trace_id, parent.span_id, parent.sampled
        else:
            tp = parse_traceparent(traceparent)
            if tp:
                trace, pid, sampled = tp
            else:
                trace, pid = uuid.uuid4().hex, ""
                sampled = opts is not None and random.random() < opts.traces_sample_rate
        sp = Span(str(name)[:200], kind, trace, pid, sampled, tags)
        sp._token = _span_ctx.set(sp)
        return sp
    except Exception:  # noqa: BLE001
        return Span("noop", "internal", uuid.uuid4().hex, "", False, None)


def finish_span(sp, status=None, name=None):
    try:
        if sp.done:
            return
        sp.done = True
        if name:
            sp.name = str(name)[:200]
        if status:
            sp.status = status
        if sp._token is not None:
            try:
                _span_ctx.reset(sp._token)
            except ValueError:  # finished from another context (async hand-off): just clear
                _span_ctx.set(None)
        s = _state["sender"]
        if sp.sampled and s is not None:
            s.put(SPANS_PATH, {"trace_id": sp.trace_id, "span_id": sp.span_id, "parent_id": sp.parent_id, "name": sp.name, "kind": sp.kind,
                               "start": sp.start, "duration_ms": (time.perf_counter() - sp._t0) * 1000.0, "status": sp.status,
                               "tags": _mtags(sp.tags), "release": _state["release"] or "", "environment": _state["environment"]})
    except Exception:  # noqa: BLE001
        pass


class span(contextlib.ContextDecorator):
    """`with linearjeph.span("db.query", tags={...}) as sp:` or `@linearjeph.span("job")`. Marks the span failed on exceptions."""

    def __init__(self, name, kind="internal", tags=None, traceparent=None):
        self.name, self.kind, self.tags, self.traceparent = name, kind, tags, traceparent
        self.sp = None

    def __enter__(self):
        self.sp = start_span(self.name, self.kind, self.tags, self.traceparent)
        return self.sp

    def __exit__(self, et, ev, tb):
        finish_span(self.sp, "error" if et is not None else None)
        return False

    def _recreate_cm(self):
        return type(self)(self.name, self.kind, self.tags, self.traceparent)


def inject_headers(headers):
    """Add a `traceparent` header for an outgoing call so the downstream service continues this trace."""
    sp = _span_ctx.get()
    if sp is not None:
        headers["traceparent"] = sp.traceparent()
    return headers


# ------------------------------------------------------------------ cron check-ins

@_safe
def checkin(name, status="ok", run_id=None, duration_ms=None, message=None, schedule=None, tz=None, grace_s=None,
            interval_s=None, max_runtime_s=None):
    """Report a job run: status "start", "ok" or "fail". Send start then ok/fail with the same run_id to get the duration.
    `schedule` (cron expression) with `grace_s` lets LINEARJEPH alert when a run is missed; `max_runtime_s` alerts on stuck runs."""
    if not client.HEARTBEAT_NAME_RE.match(str(name)):
        log.warning("linearjeph: check-in name must match [A-Za-z0-9._-]{1,64}; ignored")
        return
    body = {"monitor": str(name), "status": status if status in ("start", "ok", "fail") else "fail"}
    for k, v in (("run_id", run_id), ("duration_ms", duration_ms), ("message", message and _clean(message, 300)), ("schedule", schedule),
                 ("tz", tz), ("grace_s", grace_s), ("interval_s", interval_s), ("max_runtime_s", max_runtime_s)):
        if v not in (None, ""):
            body[k] = v
    s = _state["sender"]
    if s:
        s.put(CHECKIN_PATH, body)


class monitor(contextlib.ContextDecorator):
    """Wrap a cron job or worker: sends start, then ok (or fail with the error) with the run duration. Re-raises errors.

        @linearjeph.monitor("nightly-backup", schedule="0 2 * * *", grace_s=600)
        def run(): ...

        with linearjeph.monitor("sync", interval_s=900, capture=True): ...

    `capture=True` also reports the exception as an error event.
    """

    def __init__(self, name, interval_s=None, capture=False, schedule=None, tz=None, grace_s=None, max_runtime_s=None):
        self.name, self.capture = name, capture
        self.opts = dict(interval_s=interval_s, schedule=schedule, tz=tz, grace_s=grace_s, max_runtime_s=max_runtime_s)
        self.run_id = self._t0 = None

    def __enter__(self):
        self.run_id, self._t0 = uuid.uuid4().hex, time.perf_counter()
        checkin(self.name, "start", run_id=self.run_id, **self.opts)
        return self

    def __exit__(self, et, ev, tb):
        try:
            if et is not None and self.capture and not issubclass(et, (SystemExit, KeyboardInterrupt)):
                client.capture_exception((et, ev, tb))
            dur = (time.perf_counter() - self._t0) * 1000.0
            failed = et is not None and not (issubclass(et, SystemExit) and not ev.code)
            checkin(self.name, "fail" if failed else "ok", run_id=self.run_id, duration_ms=dur,
                    message=("%s: %s" % (et.__name__, ev)) if failed else None, **self.opts)
        except Exception:  # noqa: BLE001
            pass
        return False

    def _recreate_cm(self):
        return type(self)(self.name, self.opts["interval_s"], self.capture, self.opts["schedule"], self.opts["tz"],
                          self.opts["grace_s"], self.opts["max_runtime_s"])


# ------------------------------------------------------------------ releases and deploys

def _release_version(version):
    return version or _state["release"] or os.environ.get("LINEARJEPH_RELEASE") or ""


@_safe
def release(version=None, commit=None, notes=None):
    """Record a release marker (shown on charts; errors are linked to the release that introduced them)."""
    v = _release_version(version)
    s = _state["sender"]
    if s and v:
        s.put(RELEASES_PATH, {k: x for k, x in (("version", v), ("commit", commit), ("notes", notes)) if x})


@_safe
def deploy(version=None, environment=None, status="success", url=None, commit=None, notify=False):
    """Record a deploy of `version` (default: the release passed to init). `notify=True` also tells your alert channels."""
    v = _release_version(version)
    s = _state["sender"]
    if s and v:
        body = {"version": v, "environment": environment or _state["environment"], "status": status, "notify": bool(notify)}
        for k, x in (("url", url), ("commit", commit)):
            if x:
                body[k] = x
        s.put(DEPLOYS_PATH, body)
