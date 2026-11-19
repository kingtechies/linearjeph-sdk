"""Health check registry. Register checks, expose them on an HTTP path, and LINEARJEPH monitors poll it.

    from linearjeph import health
    health.register("database", lambda: db_ping_ms(), warn=200, fail=1000)   # a number is compared to the thresholds
    health.register("cache", lambda: redis.ping())                           # True/False
    # Flask: health.flask_route(app, "/_lj/health", token="...")   Django: path("_lj/health", health.django_view(token="..."))
    # FastAPI/ASGI: app.mount("/_lj/health", health.asgi_app(token="..."))    any WSGI: health.wsgi_app(token="...")

Then add a "SDK health check" monitor in the dashboard with that URL (and the same token). Response:
{"status": "ok|degraded|fail", "checks": {"database": {"status": "pass|warn|fail", "value": 12.5, "message": "..."}}}
HTTP 200 when ok or degraded, 503 when any check fails.
"""
import hmac
import json
import threading
import time
from concurrent.futures import ThreadPoolExecutor, TimeoutError as FutureTimeout

_checks = {}
_lock = threading.Lock()
_pool = ThreadPoolExecutor(max_workers=8, thread_name_prefix="linearjeph-health")
_outer = ThreadPoolExecutor(max_workers=4, thread_name_prefix="linearjeph-health-run")  # separate: checks wait on _pool


def register(name, fn, warn=None, fail=None, timeout=5.0, below=False):
    """Add or replace a check. `fn()` returns True/False, a number (value) or a dict {"status","value","message"}.
    `warn`/`fail` thresholds apply to numbers (higher is worse; `below=True` flips it). Raising counts as fail."""
    with _lock:
        _checks[str(name)[:60]] = {"fn": fn, "warn": warn, "fail": fail, "timeout": float(timeout), "below": bool(below)}


def unregister(name):
    with _lock:
        _checks.pop(str(name), None)


def _grade(value, c):
    worse = (lambda lim: value <= lim) if c["below"] else (lambda lim: value >= lim)
    if c["fail"] is not None and worse(c["fail"]):
        return "fail"
    if c["warn"] is not None and worse(c["warn"]):
        return "warn"
    return "pass"


def _run_one(c):
    t0 = time.perf_counter()
    fut = _pool.submit(c["fn"])
    try:
        res = fut.result(timeout=c["timeout"])
    except FutureTimeout:
        return {"status": "fail", "message": "timed out after %gs" % c["timeout"]}
    except Exception as exc:  # noqa: BLE001
        return {"status": "fail", "message": ("%s: %s" % (type(exc).__name__, exc))[:200]}
    if isinstance(res, dict):
        out = {k: res[k] for k in ("status", "value", "message") if k in res}
        if out.get("status") not in ("pass", "warn", "fail"):
            out["status"] = _grade(out["value"], c) if isinstance(out.get("value"), (int, float)) else "pass"
        return out
    if isinstance(res, bool) or res is None:
        return {"status": "pass" if res is not False else "fail", "value": round((time.perf_counter() - t0) * 1000, 1)}
    if isinstance(res, (int, float)):
        return {"status": _grade(float(res), c), "value": float(res)}
    return {"status": "pass", "message": str(res)[:200]}


def run_checks():
    """Run every registered check (in parallel, each with its own timeout) and return the response body."""
    with _lock:
        items = list(_checks.items())
    futs = {name: _outer.submit(_run_one, c) for name, c in items}
    results = {}
    for name, f in futs.items():
        try:
            results[name] = f.result(timeout=30)
        except Exception:  # noqa: BLE001
            results[name] = {"status": "fail", "message": "check crashed"}
    states = [r["status"] for r in results.values()]
    top = "fail" if "fail" in states else ("degraded" if "warn" in states else "ok")
    return {"status": top, "checks": results}


def respond(authorization=None, token=None):
    """Framework-neutral: returns (http_status, body_dict). Pass the `Authorization` header and the expected token."""
    if token and not hmac.compare_digest(str(authorization or ""), "Bearer " + token):
        return 401, {"status": "unauthorized"}
    body = run_checks()
    return (503 if body["status"] == "fail" else 200), body


def wsgi_app(token=None):
    def app(environ, start_response):
        code, body = respond(environ.get("HTTP_AUTHORIZATION"), token)
        data = json.dumps(body).encode()
        start_response("%d %s" % (code, {200: "OK", 401: "Unauthorized", 503: "Service Unavailable"}[code]),
                       [("Content-Type", "application/json"), ("Content-Length", str(len(data))), ("Cache-Control", "no-store")])
        return [data]
    return app


def django_view(token=None):
    def view(request):
        from django.http import JsonResponse
        code, body = respond(request.headers.get("Authorization"), token)
        r = JsonResponse(body, status=code)
        r["Cache-Control"] = "no-store"
        return r
    return view


def flask_route(app, path="/_lj/health", token=None):
    from flask import jsonify, request

    def _health():
        code, body = respond(request.headers.get("Authorization"), token)
        r = jsonify(body)
        r.status_code = code
        r.headers["Cache-Control"] = "no-store"
        return r
    app.add_url_rule(path, "linearjeph_health", _health)
    return app


def asgi_app(token=None):
    async def app(scope, receive, send):
        if scope["type"] != "http":
            return
        auth = dict(scope.get("headers") or []).get(b"authorization", b"").decode("latin-1")
        import asyncio
        code, body = await asyncio.get_running_loop().run_in_executor(None, respond, auth, token)
        data = json.dumps(body).encode()
        await send({"type": "http.response.start", "status": code, "headers": [(b"content-type", b"application/json"), (b"cache-control", b"no-store")]})
        await send({"type": "http.response.body", "body": data})
    return app
