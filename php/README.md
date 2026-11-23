# linearjeph/sdk (PHP)

Prophecy, the LINEARJEPH growth-intelligence SDK for PHP 8.1+. It tracks your platform's users, payments, features and API requests, scans your codebase locally into a manifest (never source code, env values or secrets), imports history and fetches the projection report. No dependencies: `ext-curl` is used when loaded (stream contexts otherwise) and `ext-zlib` gzips request bodies.

    composer require linearjeph/sdk

Without Composer: `require 'path/to/src/autoload.php';`

```php
use Linearjeph\Prophecy;

Prophecy::init([
    'ingestKey' => getenv('LINEARJEPH_INGEST_KEY'),
    'secretKey' => getenv('LINEARJEPH_SECRET_KEY'), // optional, server side only
]);

Prophecy::signup($user->id, ['plan' => 'free', 'channel' => 'ads', 'country' => 'NG']);
Prophecy::payment($user->id, 49.0, 'USD', ['plan' => 'pro']);       // negative amount = refund
Prophecy::subscription($user->id, ['plan' => 'pro', 'mrr' => 49.0, 'status' => 'active']);
Prophecy::feature('export-csv', $user->id);
Prophecy::churn($user->id, 'too expensive');
Prophecy::identify($user->id, ['company' => 'Acme']);
Prophecy::track('invoice_sent', ['uid' => $user->id, 'props' => ['count' => 3]]);
```

User ids are hashed with SHA-256 before they leave the process (`'hashUserIds' => false` to disable).

## Delivery

Events buffer in memory (bounded at 10,000) and are sent in gzip batches of 100, retried with backoff on 429/5xx. The buffer is flushed when it reaches 100 events, on `Prophecy::flush()`, and at the end of the request via `register_shutdown_function`, after `fastcgi_finish_request()` (or LiteSpeed's equivalent) so visitors never wait. Tracking calls never throw into your app. In long-running workers call `Prophecy::flush()` after each job.

## Request tracking

- Laravel: add `Linearjeph\Prophecy\Laravel\TrackRequests::class` to your global middleware (route template from `route()->uri()`).
- PSR-15 / Slim / Mezzio: `$app->add(Prophecy::middleware());` (duck typed, no psr packages needed).
- Anything else: `Prophecy::recordRequest('/users/:id', 'GET', 200, $durationMs);`

## Scan, import, report (server side)

```php
$manifest = Prophecy::scan(__DIR__);   // languages, loc, dependencies, routes, features, integrations, infra, env NAMES, tests
Prophecy::sendManifest($manifest);
Prophecy::importHistory('events', $rows, fn ($done, $total) => print("$done/$total\n"));
$report = Prophecy::report('revenue');  // or null for everything
Prophecy::refresh(); Prophecy::live(); $url = Prophecy::embedUrl(3600);
```

Secret-key calls throw `Linearjeph\Prophecy\ProphecyException` on failure.

## CLI

    LINEARJEPH_INGEST_KEY=... vendor/bin/prophecy scan . --send
    LINEARJEPH_SECRET_KEY=... vendor/bin/prophecy import events history.csv

`scan` without `--send` prints the manifest as JSON. CSV columns: `ts, uid, name, kind, feature, amount, currency, id` (customers: `uid, signup_at, plan, mrr, country, channel`); any other column becomes a prop (event) or trait (customer).

## Product API client

`LinearJeph\Products\Client` calls the Fix, Shield, Ops, Models, Agents and Comply APIs with an API key (`ljk_...`, create one in the dashboard). The key and server default to `LJ_API_KEY` and `LJ_BASE_URL` (`https://linearjeph.cc`).

```php
use LinearJeph\Products\Client;
use LinearJeph\Products\ApiError;

$lj = new Client(); // or new Client(apiKey: '...', baseUrl: '...', org: '12', timeout: 30)
$lj->whoami();

$job = $lj->fix->createJob(['source_id' => 'gh:acme/app', 'issue' => 'login crashes on empty email']);
$job = $lj->fix->wait($job['id'], 1200); // polls until done, pr_open, failed, rejected or cancelled

$scan = $lj->shield->waitVibeScan($lj->shield->vibeScan(4)['id']);
$blocking = $lj->shield->gate($scan, 'high'); // findings at or above high

$lj->ops->incidents('open'); $lj->models->endpointUsage(3); $lj->agents->action(5, 'run'); $lj->comply->recheck();
$lj->request('GET', '/api/v1/market/catalog'); // any route not wrapped

try { $lj->fix->job(999); } catch (ApiError $e) { echo $e->status(), ' ', $e->errorCode(), ' ', $e->getMessage(); }
```

Answers are arrays; lists of records are under `results`. GET requests retry 429, 5xx and network errors (3 tries); writes are never retried. `fix->jobPatch()` and `comply->export()` return the raw body. The command line (`lj`) ships with the Node and Python SDKs.

Tests: `php tests/run.php`. MIT licensed.
