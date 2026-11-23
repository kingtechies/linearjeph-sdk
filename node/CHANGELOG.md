# Changelog

## 1.3.0

Adds the product API client and the `lj` command line for the Fix, Shield, Ops, Models, Agents and Comply products. `new Client({ apiKey })` (also `@linearjeph/sdk/products`) wraps the customer API with namespaces `fix`, `shield`, `ops`, `models`, `agents`, `comply`, `whoami()`, a generic `request()`, typed `ApiError`, GET retries on 429/5xx, and helpers `fix.wait`, `shield.gate`, `shield.gateProject`, `agents.waitRun`. New binaries: `lj` (and `linearjeph <product> ...`) with `login`, `logout`, `whoami`, `--json` output, and exit codes 0 ok, 1 gate or job failed, 2 usage, auth or API error. The existing `linearjeph prophecy scan|import` commands are unchanged.

## 1.2.0

Adds Prophecy, the paid growth-intelligence module: `prophecy.init/track/feature/signup/payment/subscription/churn/identify`, Express and Next.js request middleware, a local codebase scanner (`scan`, `sendManifest`), history import, report access and signed embed URLs. Also importable as `@linearjeph/sdk/prophecy`. New CLI: `linearjeph prophecy scan [dir] [--send]` and `linearjeph prophecy import <events|customers> <file>`. See docs/sdk/prophecy.md.

## 1.1.0

Adds metrics, structured logs, tracing, cron check-ins, release and deploy markers and health checks. `monitor` now sends start, ok and fail check-ins with the run duration. Full notes: docs/sdk/CHANGELOG.md.

## 1.0.0

Error tracking, performance transactions, heartbeats and dependency reporting.
