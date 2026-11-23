# Changelog

## 1.3.0

Adds the product API client `Linearjeph::Client` (also `LinearJeph::Client`): `fix`, `shield`, `ops`, `models`, `agents` and `comply` namespaces, a generic `request`, `whoami`, a typed `ApiError`, GET retries (429 and 5xx, honours Retry-After), `fix.wait` and `shield.gate`. `Linearjeph::VERSION` is now defined. No changes to the Prophecy API.

## 1.2.0

First release. Prophecy growth intelligence for Ruby 3.0+: event, payment, subscription and feature tracking, Rack middleware (Rails and Sinatra route templates), a local codebase scanner (manifest only, never source code), history import, and report, refresh, live and embed calls. CLI: `prophecy scan` and `prophecy import`.
