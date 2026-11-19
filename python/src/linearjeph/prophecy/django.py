"""Django middleware: MIDDLEWARE = [..., "linearjeph.prophecy.django.ProphecyMiddleware"]. Records the URL route template
(request.resolver_match.route), method, status and duration. Never bodies, headers or query strings."""
import asyncio
import time

from .middleware import _record, normalize_path, route_template

try:  # Django >= 4.2 provides the helper; older versions use the asyncio marker
    from asgiref.sync import markcoroutinefunction as _mark
except ImportError:  # pragma: no cover
    def _mark(f):
        f._is_coroutine = asyncio.coroutines._is_coroutine
        return f


class ProphecyMiddleware(object):
    sync_capable = True
    async_capable = True

    def __init__(self, get_response, ignore_paths=()):
        self.get_response = get_response
        self.ignore = tuple(ignore_paths or ())
        self._async = asyncio.iscoroutinefunction(get_response)
        if self._async:
            _mark(self)

    def __call__(self, request):
        if self._async:
            return self.__acall__(request)
        t0 = time.perf_counter()
        status = 500
        try:
            response = self.get_response(request)
            status = getattr(response, "status_code", 500)
            return response
        finally:
            self._done(request, status, t0)

    async def __acall__(self, request):
        t0 = time.perf_counter()
        status = 500
        try:
            response = await self.get_response(request)
            status = getattr(response, "status_code", 500)
            return response
        finally:
            self._done(request, status, t0)

    def _done(self, request, status, t0):
        try:
            path = getattr(request, "path", "/")
            if self.ignore and path.startswith(self.ignore):
                return
            match = getattr(request, "resolver_match", None)
            route = getattr(match, "route", None)
            uid = None
            user = getattr(request, "user", None)
            if user is not None and getattr(user, "is_authenticated", False):
                uid = getattr(user, "pk", None)
            _record(route_template(route) if route else normalize_path(path), getattr(request, "method", "GET"), status,
                    (time.perf_counter() - t0) * 1000, uid)
        except Exception:  # noqa: BLE001 - never break a response
            pass
