# linearjeph Go SDK

Error tracking, request timing, cron heartbeats and dependency reporting for LINEARJEPH maintenance. Standard library only, Go 1.21+.

    go get github.com/kingtechies/linearjeph-sdk/go

```go
linearjeph.Init(linearjeph.Options{Key: "lj_app_YOUR_KEY", Release: "1.4.2"})
defer linearjeph.Flush(5 * time.Second)
http.ListenAndServe(":8080", linearjeph.Recover(mux)) // times requests, captures panics (re-panics after capture)
```

Get the key from Dashboard > Maintenance > your application. The endpoint defaults to `https://linearjeph.cc`.

- `linearjeph.CaptureError(err)`, `Capture(recover())`, `CaptureMessage`, `Heartbeat("nightly-job", "ok", 3600)`.
- Cron: `linearjeph.Monitor("nightly", 86400, func() error { ... })`.
- Dependencies: `ReportPackages()` (build info), `ReportGoSum("go.sum")`, `ReportGoMod("go.mod")`.
- Options: `SampleRate`, `TracesRate`, `BeforeSend`, `ScrubKeys`, `QueueSize`, `Retries`, `Debug`.
- Delivery is asynchronous with a bounded queue and retries with backoff; call `Flush` before exit. Nothing panics into your code.

Full documentation: `docs/sdk/go.md` in the repository, or https://linearjeph.cc/docs/sdk/go. Tests: `go test ./...`. MIT licensed.

## Prophecy (growth intelligence)

Package `github.com/kingtechies/linearjeph-sdk/go/prophecy` tracks users, payments, features and API requests, scans your codebase locally into a manifest (never source code, env values or secrets), imports history and fetches the projection report. Standard library only.

```go
import "github.com/kingtechies/linearjeph-sdk/go/prophecy"

prophecy.Init(prophecy.Options{IngestKey: os.Getenv("LINEARJEPH_INGEST_KEY"), SecretKey: os.Getenv("LINEARJEPH_SECRET_KEY")})
defer prophecy.Close(5 * time.Second)

prophecy.Signup("user-42", prophecy.SignupOptions{Plan: "free", Channel: "ads", Country: "NG"})
prophecy.Payment("user-42", 49, prophecy.PaymentOptions{Currency: "USD", Plan: "pro"})
prophecy.Feature("export-csv", "user-42")
prophecy.Subscription("user-42", "pro", 49, "active")
prophecy.Churn("user-42", "too expensive")
http.ListenAndServe(":8080", prophecy.Middleware(mux)) // route template, status, duration; use RecordRequest(route, method, status, dur) with chi or gin

m, _ := prophecy.Scan(".")   // manifest; prophecy.SendManifest(m) uploads it
report, _ := prophecy.Report("revenue") // secret key, server side only; also Refresh, Live, EmbedURL(ttl), ImportHistory(kind, rows, progress)
```

User ids are hashed with SHA-256 before they leave the process (`HashUserIDs: prophecy.Bool(false)` to disable). Tracking never panics or returns errors; events are batched (every 2 s or 100), gzipped and retried on 429/5xx with a bounded queue of 10,000.

CLI (keys from `LINEARJEPH_INGEST_KEY`, `LINEARJEPH_SECRET_KEY`, `LINEARJEPH_ENDPOINT`):

    go run github.com/kingtechies/linearjeph-sdk/go/prophecy/cmd/prophecy scan . --send
    go run github.com/kingtechies/linearjeph-sdk/go/prophecy/cmd/prophecy import events history.csv
