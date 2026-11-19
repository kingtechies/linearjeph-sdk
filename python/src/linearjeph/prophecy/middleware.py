"""Request tracking middlewares: WSGI, ASGI (FastAPI/Starlette), Flask. Records route template, method, status and
duration only. Never bodies, headers or query strings. Frameworks are imported lazily (the SDK has no hard dependencies)."""
import re
import time

_ID_SEG = re.compile(r"^(\d+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{16,}|(?=.*\d)[A-Za-z0-9_-]{20,})$")
_PARAM = re.compile(r"<(?:[^>:]+:)?([^>]+)>|\{([^}:]+)(?::[^}]*)?\}")


def normalize_path(path):
    """'/users/42/orders/9f8...' -> '/users/:id/orders/:id' (no query string, ever)."""
    clean = str(path or "/").split("?")[0].split("#")[0]
    out = "/".join(":id" if s and _ID_SEG.match(s) else s for s in clean.split("/"))
    return out or "/"


def route_template(rule):
    """Framework route pattern -> '/users/:id' ('<int:id>' and '{id}' forms; regex groups reduced)."""
    s = str(rule or "")
    s = re.sub(r"\(\?P<(\w+)>[^)]*\)", r":\1", s)
    s = _PARAM.sub(lambda m: ":" + (m.group(1) or m.group(2)), s)
    s = re.sub(r"^\^|\$$", "", s)
    if not s.startswith("/"):
        s = "/" + s
    return s


def _record(route, method, status, ms, uid=None):
    from . import record_request
    record_request(route, method, status, ms, uid)


class WSGIMiddleware(object):
    """WSGI: app.wsgi_app = WSGIMiddleware(app.wsgi_app). Apps can set environ['prophecy.route'] to give an exact template."""

    def __init__(self, app, ignore_paths=(), uid_getter=None):
        self.app = app
        self.ignore = tuple(ignore_paths or ())
        self.uid_getter = uid_getter

    def __call__(self, environ, start_response):
        t0 = time.perf_counter()
        status = {"code": 500}

        def _start(s, headers, exc_info=None):
            try:
                status["code"] = int(str(s).split(" ", 1)[0])
            except ValueError:
                pass
            return start_response(s, headers, exc_info) if exc_info else start_response(s, headers)

        try:
            result = self.app(environ, _start)
        except Exception:
            self._done(environ, status["code"], t0)
            raise
        return _Closing(result, lambda: self._done(environ, status["code"], t0))

    def _done(self, environ, code, t0):
        try:
            path = environ.get("PATH_INFO", "/")
            if self.ignore and path.startswith(self.ignore):
                return
            route = environ.get("prophecy.route") or normalize_path(environ.get("SCRIPT_NAME", "") + path)
            uid = None
            if self.uid_getter:
                try:
                    uid = self.uid_getter(environ)
                except Exception:  # noqa: BLE001
                    pass
            _record(route, environ.get("REQUEST_METHOD", "GET"), code, (time.perf_counter() - t0) * 1000, uid)
        except Exception:  # noqa: BLE001 - never break a response
            pass


class _Closing(object):
    def __init__(self, result, done):
        self._result, self._done, self._fired = result, done, False

    def __iter__(self):
        return iter(self._result)

    def close(self):
        try:
            if hasattr(self._result, "close"):
                self._result.close()
        finally:
            if not self._fired:
                self._fired = True
                self._done()


class ASGIMiddleware(object):
    """ASGI (FastAPI, Starlette, Quart...): app.add_middleware(ASGIMiddleware). The route comes from scope['route']
    (path_format) when the framework sets it, else the normalised path."""

    def __init__(self, app, ignore_paths=()):
        self.app = app
        self.ignore = tuple(ignore_paths or ())

    async def __call__(self, scope, receive, send):
        if scope.get("type") != "http":
            return await self.app(scope, receive, send)
        t0 = time.perf_counter()
        status = {"code": 500}

        async def _send(msg):
            if msg.get("type") == "http.response.start":
                status["code"] = msg.get("status", 500)
            await send(msg)

        try:
            await self.app(scope, receive, _send)
        finally:
            try:
                path = scope.get("path", "/")
                if not (self.ignore and path.startswith(self.ignore)):
                    route = scope.get("route")
                    tpl = getattr(route, "path_format", None) or getattr(route, "path", None)
                    _record(route_template(tpl) if tpl else normalize_path(path), scope.get("method", "GET"), status["code"],
                            (time.perf_counter() - t0) * 1000)
            except Exception:  # noqa: BLE001
                pass


ProphecyASGIMiddleware = ASGIMiddleware
ProphecyWSGIMiddleware = WSGIMiddleware


def init_flask(app, ignore_paths=(), uid_getter=None):
    """Flask: prophecy.init_flask(app) after prophecy.init(...). Uses request.url_rule for the route template."""
    from flask import g, request  # imported lazily: Flask is the customer's dependency
    ignore = tuple(ignore_paths or ())

    @app.before_request
    def _prophecy_start():
        g._prophecy_t0 = time.perf_counter()

    @app.after_request
    def _prophecy_end(resp):
        try:
            t0 = getattr(g, "_prophecy_t0", None)
            if t0 is not None and not (ignore and request.path.startswith(ignore)):
                rule = request.url_rule.rule if request.url_rule else None
                uid = uid_getter(request) if uid_getter else None
                _record(route_template(rule) if rule else normalize_path(request.path), request.method, resp.status_code,
                        (time.perf_counter() - t0) * 1000, uid)
        except Exception:  # noqa: BLE001
            pass
        return resp
    return app
