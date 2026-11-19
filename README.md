# LINEARJEPH SDKs: revenue forecasting, growth analytics and app monitoring

Official open-source SDKs for [LINEARJEPH](https://linearjeph.cc) in **Python, Node.js, Go, PHP, Ruby and the browser**.
Zero runtime dependencies. MIT licensed.

- **Prophecy (growth intelligence):** track signups, payments, subscriptions, feature use and API requests, scan your
  codebase locally, import your history, and get a **12-month MRR/ARR forecast with P10-P90 ranges**, churn and cohort
  retention, LTV and CAC payback, **feature impact** (which features earn money and which are dead weight), API health,
  security checks, niche benchmarks, an indicative valuation and a ranked 90-day plan. Built on statistics and simulation
  (time-series forecasting checked against your own past, Kaplan-Meier survival, Monte Carlo), not guesswork.
  [See a sample report](https://linearjeph.cc/prophecy).
- **Maintenance:** error tracking, performance timing, logs, tracing, cron heartbeats and dependency reporting.

## Install

| Language | Package | Install | Folder |
|---|---|---|---|
| Python 3.8+ | [`linearjeph` on PyPI](https://pypi.org/project/linearjeph/) | `pip install linearjeph` | [`python/`](python) |
| Node.js 18+ | `@linearjeph/sdk` | `npm install https://linearjeph.cc/sdk/linearjeph-sdk-1.2.0.tgz` | [`node/`](node) |
| Go 1.21+ | `github.com/kingtechies/linearjeph-sdk/go` | `go get github.com/kingtechies/linearjeph-sdk/go@v1.2.0` | [`go/`](go) |
| PHP 8.1+ / Laravel | `linearjeph/sdk` | `composer config repositories.linearjeph vcs https://github.com/kingtechies/linearjeph-php && composer require linearjeph/sdk:^1.2` | [`php/`](php) |
| Ruby 3.0+ / Rails | `linearjeph` gem | `curl -LO https://linearjeph.cc/sdk/linearjeph-1.2.0.gem && gem install ./linearjeph-1.2.0.gem` | [`ruby/`](ruby) |
| Browser | `prophecy.js` | `<script src="https://linearjeph.cc/sdk/prophecy.js"></script>` | — |

Every package can also be downloaded from https://linearjeph.cc/sdk/ (checksums: https://linearjeph.cc/sdk/SHA256SUMS).

## Prophecy in 30 seconds

```python
from linearjeph import prophecy

prophecy.init(ingest_key="lj_app_YOUR_KEY", secret_key="ljp_sk_YOUR_SECRET")  # secret key: server side only
prophecy.signup(user.id, plan="free", channel="google")
prophecy.feature("export-pdf", user.id)
prophecy.payment(user.id, 49.00, "USD", plan="pro")

print(prophecy.report()["report"]["headline"]["verdict"])
# "MRR $21.4k, growing 6.7% a month; forecast $39.4k MRR in 12 months (range $23.1k-$67.1k). ..."
```

The same functions exist in every SDK (`init`, `track`, `feature`, `signup`, `payment`, `subscription`, `churn`,
`identify`, `middleware`, `scan`, `importHistory`, `report`, `refresh`, `live`, `embedUrl`). The codebase scanner sends a
manifest of routes, dependencies and infrastructure, **never your source code or secret values**, and user ids are
hashed with SHA-256 before they leave your server.

Guide: https://linearjeph.cc/docs/sdk/prophecy · Monitoring docs: https://linearjeph.cc/docs/sdk

## Versions

Releases are tagged per SDK: `python/vX.Y.Z`, `node/vX.Y.Z`, `go/vX.Y.Z`, `php/vX.Y.Z`, `ruby/vX.Y.Z`. The PHP package is
also published from [kingtechies/linearjeph-php](https://github.com/kingtechies/linearjeph-php) (Packagist needs the
package at a repository root).

Keys live in Dashboard → Maintenance (ingest key) and Dashboard → Prophecy (secret key). Never commit them; load them from
environment variables.
