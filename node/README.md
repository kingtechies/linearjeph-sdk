# @linearjeph/sdk (Node.js)

Error tracking, performance timing, cron heartbeats and dependency reporting for LINEARJEPH maintenance. No runtime dependencies, Node 18+, CommonJS + ESM + TypeScript types.

    npm install @linearjeph/sdk

```js
const linearjeph = require("@linearjeph/sdk");
linearjeph.init({ key: "lj_app_YOUR_KEY", release: "1.4.2", environment: "production" });
linearjeph.captureException(new Error("boom"));   // uncaught exceptions are captured automatically
```

Get the key from Dashboard > Maintenance > your application. The endpoint defaults to `https://linearjeph.cc`.

- Express: `app.use(linearjeph.requestHandler())` first, `app.use(linearjeph.errorHandler())` after the routes.
- `captureMessage`, `heartbeat("nightly-job", "ok", 3600)`, `reportPackages()` (reads `package-lock.json`).
- Cron: `await linearjeph.monitor("nightly", job, { intervalS: 86400, flush: true })`.
- Performance: `await linearjeph.withTransaction("checkout", fn)` or `startTransaction(name).end()`.
- Options: `sampleRate`, `tracesSampleRate`, `beforeSend`, `scrubKeys`, `maxQueue`, `retries`, `debug`.
- Sending is asynchronous with a bounded queue, retries with backoff, and a flush on `beforeExit`. The SDK never throws into your code.

Full documentation: `docs/sdk/node.md` in the repository, or https://linearjeph.cc/docs/sdk/node. Tests: `npm test`. MIT licensed.
