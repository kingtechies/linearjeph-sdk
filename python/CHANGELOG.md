# Changelog

## 1.3.0

Adds the product SDK client and the `lj` command line. `from linearjeph import Client` gives `fix`, `shield`, `ops`, `models`, `agents` and `comply` namespaces over the customer API with an `ljk_` API key (`LJ_API_KEY`, `LJ_BASE_URL`), typed `ApiError`, retries for GET on 429 and 5xx, `client.request()` for anything unwrapped, and helpers `fix.wait`, `shield.wait_scan`, `shield.gate`, `agents.wait_run`. New console scripts `lj` and `linearjeph-cli` (login, logout, whoami, fix, shield, ops, models, agents, comply, api) with `--json` and CI exit codes (0 ok, 1 gate or job failed, 2 usage or auth error). Credentials are stored in `~/.config/linearjeph/credentials` with mode 0600. No new dependencies.

## 1.2.0

Adds Prophecy, the paid growth-intelligence module: `linearjeph.prophecy` (`init`, `track`, `feature`, `signup`, `payment`, `subscription`, `churn`, `identify`), WSGI, ASGI, Django and Flask request middleware, a local codebase scanner (`scan`, `send_manifest`), history import, report access and signed embed URLs. New CLI: `python -m linearjeph.prophecy scan [dir] [--send]` and `python -m linearjeph.prophecy import <events|customers> <file>`. See docs/sdk/prophecy.md.

## 1.1.0

Adds metrics, structured logs, tracing, cron check-ins, release and deploy markers and health checks. `monitor` now sends start, ok and fail check-ins with the run duration. Full notes: docs/sdk/CHANGELOG.md.

## 1.0.0

Error tracking, performance transactions, heartbeats and dependency reporting.
