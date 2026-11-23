# Changelog

## 1.3.0

Adds the product API client in the new `products` subpackage: `products.New(products.Options{...})` with the `Fix`, `Shield`, `Ops`, `Models`, `Agents` and `Comply` namespaces, a generic `Request`, `Whoami`, a typed `*ApiError`, GET retries (429 and 5xx, honours Retry-After), `Fix.Wait` and `Shield.Gate`. Adds a small `lj` command for CI (`whoami`, `shield gate`, `fix run --wait`) in `cmd/lj`. No changes to the existing API.

## 1.2.0

Adds Prophecy (growth intelligence) in the new `prophecy` subpackage: event, payment and feature tracking, `prophecy.Middleware`, a local codebase scanner (`prophecy.Scan`, manifest only, never source), history import, and report, refresh, live and embed calls. CLI: `go run github.com/kingtechies/linearjeph-sdk/go/prophecy/cmd/prophecy scan [dir] [--send]`. No changes to the existing API.

## 1.1.0

Adds metrics, structured logs, tracing, cron check-ins, release and deploy markers and health checks. `monitor` now sends start, ok and fail check-ins with the run duration. Full notes: docs/sdk/CHANGELOG.md.

## 1.0.0

Error tracking, performance transactions, heartbeats and dependency reporting.
