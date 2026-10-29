# linearjeph (Python)

Error tracking, performance timing, cron heartbeats and dependency reporting for LINEARJEPH maintenance. No runtime dependencies, Python 3.8+.

    pip install https://linearjeph.cc/sdk/linearjeph-1.0.0.tar.gz

```python
import linearjeph
linearjeph.init("lj_app_YOUR_KEY", release="1.4.2", environment="production")
linearjeph.capture_exception()  # or let unhandled exceptions be captured automatically
```

Get the key from Dashboard > Maintenance > your application. The endpoint defaults to `https://linearjeph.cc`.

- `capture_message("text")`, `heartbeat("nightly-job", "ok", interval_s=3600)`, `report_packages()`.
- Cron: `@linearjeph.monitor("nightly", interval_s=86400)` sends ok/error automatically.
- Performance: `with linearjeph.start_transaction("checkout"): ...`
- Django: add `"linearjeph.integrations.django.LinearjephMiddleware"` to `MIDDLEWARE`.
- FastAPI/Starlette: `app.add_middleware(linearjeph.integrations.asgi.LinearjephASGIMiddleware)`.
- Flask: `linearjeph.integrations.flask.init_app(app)`.
- Options: `sample_rate`, `traces_sample_rate`, `before_send`, `scrub_keys`, `max_queue`, `retries`, `debug`.
- Events are sent from a background thread (bounded queue, retry with backoff, 401 stops sending) and flushed at exit. The SDK never raises into your code.

Full documentation: `docs/sdk/python.md` in the repository, or https://linearjeph.cc/docs/sdk/python. Tests: `python -m pytest packages/sdk-python`. MIT licensed.
