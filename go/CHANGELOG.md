# Changelog

## 1.2.0

Adds Prophecy (growth intelligence) in the new `prophecy` subpackage: event, payment and feature tracking, `prophecy.Middleware`, a local codebase scanner (`prophecy.Scan`, manifest only, never source), history import, and report, refresh, live and embed calls. CLI: `go run github.com/kingtechies/linearjeph-sdk/go/prophecy/cmd/prophecy scan [dir] [--send]`. No changes to the existing API.

## 1.1.0

Adds metrics, structured logs, tracing, cron check-ins, release and deploy markers and health checks. `monitor` now sends start, ok and fail check-ins with the run duration. Full notes: docs/sdk/CHANGELOG.md.

## 1.0.0

Error tracking, performance transactions, heartbeats and dependency reporting.
