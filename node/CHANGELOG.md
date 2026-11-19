# Changelog

## 1.2.0

Adds Prophecy, the paid growth-intelligence module: `prophecy.init/track/feature/signup/payment/subscription/churn/identify`, Express and Next.js request middleware, a local codebase scanner (`scan`, `sendManifest`), history import, report access and signed embed URLs. Also importable as `@linearjeph/sdk/prophecy`. New CLI: `linearjeph prophecy scan [dir] [--send]` and `linearjeph prophecy import <events|customers> <file>`. See docs/sdk/prophecy.md.

## 1.1.0

Adds metrics, structured logs, tracing, cron check-ins, release and deploy markers and health checks. `monitor` now sends start, ok and fail check-ins with the run duration. Full notes: docs/sdk/CHANGELOG.md.

## 1.0.0

Error tracking, performance transactions, heartbeats and dependency reporting.
