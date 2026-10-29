"""Pure-ASGI middleware for FastAPI, Starlette and any ASGI app: `app.add_middleware(LinearjephASGIMiddleware)`."""
import time

from .. import client


class LinearjephASGIMiddleware(object):
    def __init__(self, app, ignore_paths=()):
        self.app = app
        self.ignore = tuple(ignore_paths or ())

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.app(scope, receive, send)
        t0, status = time.perf_counter(), {"code": 500}

        async def _send(msg):
            if msg["type"] == "http.response.start":
                status["code"] = msg["status"]
            await send(msg)

        try:
            await self.app(scope, receive, _send)
        except Exception as exc:
            client.capture_exception(exc)
            raise
        finally:
            try:
                path = scope.get("path", "/")
                if not (self.ignore and path.startswith(self.ignore)):
                    route = scope.get("route")  # FastAPI/Starlette set the matched route: low-cardinality names
                    name = getattr(route, "path", None) or path
                    client.capture_transaction("%s %s" % (scope.get("method", "GET"), name),
                                               (time.perf_counter() - t0) * 1000,
                                               "ok" if status["code"] < 500 else "error",
                                               tags={"http.status_code": str(status["code"])})
            except Exception:  # noqa: BLE001
                pass
