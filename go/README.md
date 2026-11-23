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

## Product API client

Package `github.com/kingtechies/linearjeph-sdk/go/products` calls the Fix, Shield, Ops, Models, Agents and Comply APIs with an API key (`ljk_...`, create one in the dashboard). Standard library only. The key and server default to `LJ_API_KEY` and `LJ_BASE_URL` (`https://linearjeph.cc`).

```go
import "github.com/kingtechies/linearjeph-sdk/go/products"

c, err := products.New(products.Options{}) // or Options{APIKey: ..., Org: "12", Timeout: 30 * time.Second}
who, _ := c.Whoami(ctx)

job, _ := c.Fix.CreateJob(ctx, products.JSON{"source_id": "gh:acme/app", "issue": "login crashes on empty email"})
job, err = c.Fix.Wait(ctx, products.Int(job, "id"), 20*time.Minute) // polls until done, pr_open, failed, rejected or cancelled

started, _ := c.Shield.VibeScan(ctx, 4)
scan, _ := c.Shield.WaitVibeScan(ctx, products.Int(started, "id"), 10*time.Minute)
blocking, _ := c.Shield.Gate(scan, "high") // findings at or above high

c.Ops.Incidents(ctx, "open"); c.Models.EndpointUsage(ctx, 3); c.Agents.Action(ctx, 5, "run", nil); c.Comply.Recheck(ctx)
c.Request(ctx, "GET", "/api/v1/market/catalog", nil, nil) // any route not wrapped
```

Every answer is a `products.JSON` (`map[string]any`); lists are under `"results"` (`products.Results`). Non-2xx answers return `*products.ApiError` (`Status`, `Code`, `Message`). GET requests retry 429 and 5xx up to 3 tries; writes are never retried. `Fix.JobPatch` and `Comply.Export` return raw bytes.

A small CLI for CI (the full `lj` ships with the Node and Python SDKs):

    go run github.com/kingtechies/linearjeph-sdk/go/cmd/lj@v1.3.0 shield gate --project 4 --fail-on high
    lj fix run --project 2 --issue "login crashes" --wait
    lj whoami --json

Key from `--key`, then `LJ_API_KEY`, then `~/.config/linearjeph/credentials`. Exit codes: 0 ok, 1 gate or job failed, 2 usage or auth error.
