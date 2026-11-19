# linearjeph: revenue forecasting, growth analytics and monitoring for Python apps

The official Python SDK for [LINEARJEPH](https://linearjeph.cc). One package, no runtime dependencies, Python 3.8+.

- **Prophecy** (growth intelligence): track signups, payments, subscriptions and feature use, scan your codebase locally,
  import your history, and get a 12-month **MRR/ARR forecast with P10-P90 ranges**, churn and cohort retention, LTV,
  feature impact (which features earn and which are dead weight), API health, security checks, an indicative valuation
  and a ranked 90-day plan. Statistics and simulation, not guesswork. [See a sample report](https://linearjeph.cc/prophecy).
- **Maintenance**: error tracking, performance timing, logs, cron heartbeats and dependency reporting.
- Works with Django, Flask, FastAPI/Starlette and any WSGI or ASGI app.

```
pip install linearjeph
```

## Prophecy quick start

```python
from linearjeph import prophecy

prophecy.init(ingest_key="lj_app_YOUR_KEY", secret_key="ljp_sk_YOUR_SECRET")  # secret: server side only
prophecy.signup(user.id, plan="free", channel="google")
prophecy.feature("export-pdf", user.id)
prophecy.payment(user.id, 49.00, "USD", plan="pro")

report = prophecy.report()["report"]  # the latest projection
print(report["headline"]["verdict"])
```

Scan the codebase (sends a manifest of routes, dependencies and infrastructure, never your source code):
`python -m linearjeph.prophecy scan . --send`. Full guide: https://linearjeph.cc/docs/sdk/prophecy

## Monitoring quick start

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

## Prophecy (growth intelligence, $500/month)

Tracks users, payments, feature use and API traffic, scans your codebase locally and returns a forecast report. Full guide: https://linearjeph.cc/docs/sdk/prophecy

```python
from linearjeph import prophecy
prophecy.init(ingest_key=os.environ["LINEARJEPH_INGEST_KEY"], secret_key=os.environ.get("LINEARJEPH_SECRET_KEY"))
prophecy.signup(user.id, plan="free", channel="ads")
prophecy.payment(user.id, 29, "USD", plan="pro")
prophecy.feature("export-csv", user.id)
prophecy.send_manifest(prophecy.scan("."))
report = prophecy.report()            # secret key, server side only
```

Requests: Django `MIDDLEWARE += ["linearjeph.prophecy.django.ProphecyMiddleware"]`, Flask `prophecy.middleware.init_flask(app)`, FastAPI/Starlette `app.add_middleware(prophecy.middleware.ASGIMiddleware)`, any WSGI app `prophecy.middleware.WSGIMiddleware(app)`. CLI: `python -m linearjeph.prophecy scan [dir] [--send]`, `python -m linearjeph.prophecy import events history.csv`. User ids are SHA-256 hashed by default.
