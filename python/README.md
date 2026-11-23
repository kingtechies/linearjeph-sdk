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

## Product SDKs and the lj command

Free with the Fix, Shield, Ops, Models, Agents and Comply products. Create an API key (`ljk_...`) in the dashboard under API keys, with the scopes you need.

```python
from linearjeph import Client
lj = Client()                                    # reads LJ_API_KEY and LJ_BASE_URL
job = lj.fix.create_job(source_id="gh:acme/app", issue="Checkout returns 500 for empty carts")
job = lj.fix.wait(job["id"], timeout=1800)       # done, accepted, failed, rejected, cancelled, or pr_open
scan = lj.shield.wait_scan(lj.shield.vibe_scan(12)["id"])
blocking = lj.shield.gate(scan, fail_on="high")  # findings at or above high
lj.request("GET", "fixops/jobs", query={"status": "failed"})   # anything not wrapped
```

Errors raise `linearjeph.ApiError` with `status`, `code` and `message`. GET requests retry on 429 and 5xx (3 tries).

```
pip install linearjeph
lj login                                     # saves the key to ~/.config/linearjeph/credentials (mode 0600)
lj whoami
lj fix run --project 3 --issue "Login fails on Safari" --wait
lj shield gate --project 12 --fail-on high   # exit 1 if any finding is high or critical
lj shield findings --severity medium
lj ops status | lj ops incidents | lj ops backup 4
lj models list | lj agents run 7 --wait | lj comply export --out audit.zip
lj api GET fixops/jobs --json
```

Exit codes: 0 ok, 1 gate or job failed, 2 usage, auth or API error. Add `--json` for raw JSON. The key comes from `--key`, then `LJ_API_KEY`, then the credentials file.

CI example (GitHub Actions):

```yaml
- run: pip install linearjeph
- run: lj shield gate --project ${{ vars.LJ_SHIELD_PROJECT }} --fail-on high
  env:
    LJ_API_KEY: ${{ secrets.LJ_API_KEY }}
```

GitLab CI: `script: [pip install linearjeph, "lj fix run --project $LJ_PROJECT --issue \"$CI_COMMIT_TITLE\" --wait"]` with `LJ_API_KEY` as a masked variable.
