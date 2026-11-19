# linearjeph (Ruby)

Prophecy, the LINEARJEPH growth-intelligence SDK for Ruby 3.0+. It tracks your platform's users, payments, features and API requests, scans your codebase locally into a manifest (never source code, env values or secrets), imports history and fetches the projection report. Standard library only (`net/http`, `json`, `zlib`, `digest`).

    gem install linearjeph        # or: gem "linearjeph" in your Gemfile

```ruby
require "linearjeph"

Linearjeph::Prophecy.init(
  ingest_key: ENV["LINEARJEPH_INGEST_KEY"],
  secret_key: ENV["LINEARJEPH_SECRET_KEY"] # optional, server side only
)

Linearjeph::Prophecy.signup(user.id, plan: "free", channel: "ads", country: "NG")
Linearjeph::Prophecy.payment(user.id, 49.0, "USD", plan: "pro")   # negative amount = refund
Linearjeph::Prophecy.subscription(user.id, plan: "pro", mrr: 49.0, status: "active")
Linearjeph::Prophecy.feature("export-csv", user.id)
Linearjeph::Prophecy.churn(user.id, "too expensive")
Linearjeph::Prophecy.identify(user.id, company: "Acme")
Linearjeph::Prophecy.track("invoice_sent", uid: user.id, props: { count: 3 })
```

User ids are hashed with SHA-256 before they leave the process (`hash_user_ids: false` to disable).

## Delivery

Tracking calls enqueue and return. A background thread sends gzip batches every 2 seconds or 100 events (bounded queue of 10,000, retries with backoff on 429/5xx) and everything is flushed at exit; the thread restarts after a fork (Puma, Unicorn). Tracking never raises into your app. Call `Linearjeph::Prophecy.flush` to force delivery.

## Request tracking

    # config/application.rb (Rails) or config.ru (Sinatra/Rack)
    config.middleware.use Linearjeph::Prophecy::Rack

Route templates come from `env["action_dispatch.route_uri_pattern"]` (Rails), `env["sinatra.route"]`, or the path with ids normalised to `:id`. Or call `Linearjeph::Prophecy.record_request("/users/:id", "GET", 200, duration_ms)`.

## Scan, import, report (server side)

```ruby
manifest = Linearjeph::Prophecy.scan(Rails.root.to_s)   # languages, loc, dependencies, routes, features, integrations, infra, env NAMES, tests
Linearjeph::Prophecy.send_manifest(manifest)
Linearjeph::Prophecy.import_history("events", rows) { |done, total| puts "#{done}/#{total}" }
report = Linearjeph::Prophecy.report("revenue")          # or nil for everything
Linearjeph::Prophecy.refresh; Linearjeph::Prophecy.live; url = Linearjeph::Prophecy.embed_url(3600)
```

Secret-key calls raise `Linearjeph::Prophecy::Error` on failure.

## CLI

    LINEARJEPH_INGEST_KEY=... prophecy scan . --send
    LINEARJEPH_SECRET_KEY=... prophecy import events history.csv

`scan` without `--send` prints the manifest as JSON. CSV columns: `ts, uid, name, kind, feature, amount, currency, id` (customers: `uid, signup_at, plan, mrr, country, channel`); any other column becomes a prop (event) or trait (customer).

MIT licensed.
