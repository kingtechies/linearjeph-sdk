"""Flask integration: `init_app(app)` after `linearjeph.init(...)`. Times requests and captures unhandled exceptions."""
import time

from .. import client


def init_app(app, ignore_paths=()):
    from flask import g, got_request_exception, request  # imported lazily: Flask is the customer's dependency

    ignore = tuple(ignore_paths or ())

    @app.before_request
    def _lj_start():
        g._lj_t0 = time.perf_counter()

    @app.after_request
    def _lj_end(resp):
        try:
            t0 = getattr(g, "_lj_t0", None)
            if t0 is not None and not (ignore and request.path.startswith(ignore)):
                name = request.url_rule.rule if request.url_rule else request.path
                client.capture_transaction("%s %s" % (request.method, name), (time.perf_counter() - t0) * 1000,
                                           "ok" if resp.status_code < 500 else "error",
                                           tags={"http.status_code": str(resp.status_code)})
        except Exception:  # noqa: BLE001
            pass
        return resp

    def _lj_exc(sender, exception, **extra):
        client.capture_exception(exception)

    got_request_exception.connect(_lj_exc, app)
    return app
