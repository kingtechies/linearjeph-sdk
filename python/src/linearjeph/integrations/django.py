"""Django middleware: request timing (transactions) and unhandled view exceptions.

    MIDDLEWARE = ["linearjeph.integrations.django.LinearjephMiddleware", ...]   # near the top

Optional setting: LINEARJEPH_IGNORE_PATHS = ("/health", "/static/") (path prefixes that are not timed).
"""
import time

from .. import client, telemetry


class LinearjephMiddleware(object):
    sync_capable = True
    async_capable = False

    def __init__(self, get_response):
        self.get_response = get_response
        try:
            from django.conf import settings
            self.ignore = tuple(getattr(settings, "LINEARJEPH_IGNORE_PATHS", ()) or ())
        except Exception:  # noqa: BLE001
            self.ignore = ()

    def __call__(self, request):
        t0 = time.perf_counter()
        sp = telemetry.start_span("request", "server", traceparent=request.META.get("HTTP_TRACEPARENT"))
        try:
            response = self.get_response(request)
        except Exception:
            self._time(request, t0, 500, sp)
            raise
        self._time(request, t0, getattr(response, "status_code", 200), sp)
        return response

    def _time(self, request, t0, code, sp=None):
        try:
            if sp is not None:
                m0 = getattr(request, "resolver_match", None)
                telemetry.finish_span(sp, "ok" if code < 500 else "error", "%s %s" % (request.method, (getattr(m0, "route", None) or request.path)))
        except Exception:  # noqa: BLE001
            pass
        try:
            if self.ignore and request.path.startswith(self.ignore):
                return
            m = getattr(request, "resolver_match", None)
            name = (getattr(m, "route", None) or request.path) if m else request.path
            name = name if name.startswith("/") else "/" + name
            client.capture_transaction("%s %s" % (request.method, name), (time.perf_counter() - t0) * 1000,
                                       "ok" if code < 500 else "error", tags={"http.status_code": str(code)})
        except Exception:  # noqa: BLE001
            pass

    def process_exception(self, request, exception):
        client.capture_exception(exception)
        return None
