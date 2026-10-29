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
