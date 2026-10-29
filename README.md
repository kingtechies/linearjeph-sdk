# LINEARJEPH SDKs

Official error tracking, performance, heartbeat and dependency-audit SDKs for [LINEARJEPH](https://linearjeph.cc) maintenance plans. Zero runtime dependencies. MIT licensed.

| Language | Folder | Install |
|---|---|---|
| Python 3.8+ | [`python/`](python) | `pip install https://linearjeph.cc/sdk/linearjeph-1.0.0.tar.gz` |
| Node.js 18+ | [`node/`](node) | `npm install https://linearjeph.cc/sdk/linearjeph-sdk-1.0.0.tgz` |
| Go 1.21+ | [`go/`](go) | `go get github.com/kingtechies/linearjeph-sdk/go@v1.0.0` |

Documentation: https://linearjeph.cc/docs/sdk

Your ingest key is in Dashboard → Maintenance → your application. Never commit it; load it from an environment variable.

Releases are tagged per SDK: `python/vX.Y.Z`, `node/vX.Y.Z`, `go/vX.Y.Z`. Checksums for the downloadable packages: https://linearjeph.cc/sdk/SHA256SUMS
