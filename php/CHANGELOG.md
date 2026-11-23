# Changelog

## 1.3.0

Adds the product API client `LinearJeph\Products\Client`: `fix`, `shield`, `ops`, `models`, `agents` and `comply` namespaces, a generic `request()`, `whoami()`, a typed `ApiError`, GET retries (429 and 5xx, honours Retry-After), `fix->wait()` and `shield->gate()`. The Composer autoloader now maps `LinearJeph\` as well as `Linearjeph\`. No changes to the Prophecy API.

## 1.2.0

First release. Prophecy growth intelligence for PHP 8.1+: event, payment, subscription and feature tracking, request middleware (PSR-15 style and Laravel), a local codebase scanner (manifest only, never source code), history import, and report, refresh, live and embed calls. CLI: `bin/prophecy`.
