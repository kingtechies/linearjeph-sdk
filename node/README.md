# @linearjeph/sdk: revenue forecasting, growth analytics and monitoring for Node.js

The official Node.js SDK for [LINEARJEPH](https://linearjeph.cc). No runtime dependencies, Node 18+, CommonJS + ESM +
TypeScript types.

- **Prophecy** (growth intelligence): track signups, payments, subscriptions and feature use, scan your codebase locally,
  import your history, and get a 12-month **MRR/ARR forecast with P10-P90 ranges**, churn and cohort retention, LTV,
  feature impact (which features earn and which are dead weight), API health, security checks, an indicative valuation
  and a ranked 90-day plan. Statistics and simulation, not guesswork. [See a sample report](https://linearjeph.cc/prophecy).
- **Maintenance**: error tracking, performance timing, logs, tracing, cron heartbeats and dependency reporting.
- Express/Connect middleware and Next.js route handler helpers.

```
npm install @linearjeph/sdk
```

## Prophecy quick start

```js
const { prophecy } = require("@linearjeph/sdk");

prophecy.init({ ingestKey: "lj_app_YOUR_KEY", secretKey: process.env.LINEARJEPH_SECRET_KEY }); // secret: server only
app.use(prophecy.middleware());                       // API routes, status and timing (no bodies)
prophecy.signup(user.id, { plan: "free", channel: "google" });
prophecy.feature("export-pdf", user.id);
prophecy.payment(user.id, 49, "USD", { plan: "pro" });

const { report } = await prophecy.report();           // the latest projection
console.log(report.headline.verdict);
```

Scan the codebase (sends a manifest of routes, dependencies and infrastructure, never your source code):
`npx linearjeph prophecy scan . --send`. Full guide: https://linearjeph.cc/docs/sdk/prophecy

## Monitoring quick start

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

## Prophecy (growth intelligence, $500/month)

Tracks users, payments, feature use and API traffic, scans your codebase locally and returns a forecast report. Full guide: https://linearjeph.cc/docs/sdk/prophecy

```js
const { prophecy } = require("@linearjeph/sdk");   // or: import { prophecy } from "@linearjeph/sdk"
prophecy.init({ ingestKey: process.env.LINEARJEPH_INGEST_KEY, secretKey: process.env.LINEARJEPH_SECRET_KEY });
app.use(prophecy.middleware());                      // route template, method, status, duration only
prophecy.signup(user.id, { plan: "free", channel: "ads" });
prophecy.payment(user.id, 29, "USD", { plan: "pro" });
prophecy.feature("export-csv", user.id);
await prophecy.sendManifest(prophecy.scan("."));     // or: npx @linearjeph/sdk prophecy scan --send
const { report } = await prophecy.report();          // secret key, server side only
```

CLI: `npx @linearjeph/sdk prophecy scan [dir] [--send]`, `npx @linearjeph/sdk prophecy import events history.csv`. User ids are SHA-256 hashed by default.
