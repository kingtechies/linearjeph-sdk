# Changelog

## 1.2.0

Adds Prophecy, the paid growth-intelligence module: `linearjeph.prophecy` (`init`, `track`, `feature`, `signup`, `payment`, `subscription`, `churn`, `identify`), WSGI, ASGI, Django and Flask request middleware, a local codebase scanner (`scan`, `send_manifest`), history import, report access and signed embed URLs. New CLI: `python -m linearjeph.prophecy scan [dir] [--send]` and `python -m linearjeph.prophecy import <events|customers> <file>`. See docs/sdk/prophecy.md.

## 1.1.0

Adds metrics, structured logs, tracing, cron check-ins, release and deploy markers and health checks. `monitor` now sends start, ok and fail check-ins with the run duration. Full notes: docs/sdk/CHANGELOG.md.

## 1.0.0

Error tracking, performance transactions, heartbeats and dependency reporting.
