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

## Product SDKs and the lj command

The same package ships an API client and a command line for the LINEARJEPH products Fix, Shield, Ops, Models, Agents and Comply. Create an API key in the dashboard (API keys); it only works for products your organization has.

```js
const { Client } = require("@linearjeph/sdk");        // or: import { Client } from "@linearjeph/sdk/products"
const lj = new Client({ apiKey: process.env.LJ_API_KEY });   // baseUrl defaults to https://linearjeph.cc (LJ_BASE_URL)

const job = await lj.fix.run(12, { issue: "Checkout returns 500 when the cart is empty" });
const done = await lj.fix.wait(job.id, { timeout: 900 });     // done | accepted | failed | rejected | cancelled | pr_open

const queued = await lj.shield.vibe.scan(12);
const scan = await lj.shield.vibe.wait(queued.id);
const bad = lj.shield.gate(scan, "high");                      // findings at or above "high"

await lj.request("GET", "opsagent/summary");                   // any route not wrapped
```

Namespaces: `fix`, `shield`, `ops`, `models`, `agents`, `comply`, plus `whoami()` and `request(method, path, body, query)`. Errors are `ApiError` with `status`, `code` and `message`. GET requests retry on 429 and 5xx (3 tries, honouring `Retry-After`). The method to route table is `ROUTES` in `products.cjs`.

```
lj login                              # saves the key to ~/.config/linearjeph/credentials (mode 0600, XDG_CONFIG_HOME respected)
lj whoami
lj fix run --project 12 --issue "Checkout returns 500" --wait
lj shield gate --project 12 --fail-on high
lj ops status | lj ops incidents | lj ops backup 3 --wait | lj ops drill 3 --wait
lj models list | lj agents run 7 --wait | lj comply export --out audit.zip
lj api GET fixops/jobs
```

`lj --help` and `lj <product> --help` list every command. Add `--json` for raw JSON. The key comes from `--key`, then `LJ_API_KEY`, then the saved credentials, and is never printed. Exit codes: 0 ok, 1 gate or job or scan failed, 2 usage, auth, API or network error. `linearjeph <product> ...` is the same command.

CI (GitHub Actions), failing the build on high or critical findings:

```yaml
jobs:
  shield:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-node@v4
        with: { node-version: 20 }
      - run: npx -y @linearjeph/sdk@1.3.0 shield gate --project 12 --fail-on high
        env:
          LJ_API_KEY: ${{ secrets.LJ_API_KEY }}
```

With `npx` the command is `linearjeph shield gate ...` (the package's first binary); after `npm i -g @linearjeph/sdk` use `lj`.
