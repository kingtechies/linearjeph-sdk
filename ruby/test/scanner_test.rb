# frozen_string_literal: true

require_relative "test_helper"

class ScannerTest < Minitest::Test
  def put(root, rel, body)
    p = File.join(root, rel)
    FileUtils.mkdir_p(File.dirname(p))
    File.write(p, body)
  end

  def fixture(base)
    root = File.join(base, "shop")
    put root, "go.mod", "module x\n\ngo 1.21\n\nrequire (\n\tgithub.com/go-chi/chi/v5 v5.0.10\n\tgithub.com/stripe/stripe-go/v76 v76.1.0\n\tgolang.org/x/sys v0.1.0 // indirect\n)\n"
    put root, "go.sum", "x"
    put root, "main.go", "package main\nfunc main() {\n\tmux.HandleFunc(\"GET /health\", h)\n\tr.Get(\"/api/v1/orders/{id}\", h)\n\tapi := r.Group(\"/api\")\n\tapi.Post(\"/invoices\", h)\n\tg.GET(\"/ping\", h)\n\tx := os.Getenv(\"STRIPE_SECRET_KEY\")\n}\n"
    put root, "routes/web.php", "<?php\nRoute::get('/posts/{post}', f);\nRoute::resource('photos', P::class);\nif (Feature::active('beta-dash')) {}\n$k = env('FEATURE_DARK_MODE');\n"
    put root, "config/routes.rb", "Rails.application.routes.draw do\n  namespace :admin do\n    resources :users\n    get '/stats', to: 'x#y'\n  end\n  post '/login', to: 's#c'\nend\n"
    put root, "web/server.js", "app.get('/express/items', h)\nconst k = process.env.SENTRY_DSN\n"
    put root, "app/dashboard/page.tsx", "export default 1\n"
    put root, "pyapp/urls.py", "urlpatterns = [path('articles/<int:pk>/', v), path('', v)]\n"
    put root, "package.json", '{"dependencies":{"express":"^4.18.0","react":"18.2.0"},"devDependencies":{"jest":"29.0.0"}}'
    put root, "composer.json", '{"require":{"php":">=8.1","laravel/framework":"^10.0","ext-json":"*"}}'
    put root, "Gemfile.lock", "GEM\n  remote: https://rubygems.org/\n  specs:\n    rails (7.1.0)\n      actioncable (= 7.1.0)\n    rspec (3.12.0)\n\nPLATFORMS\n  ruby\n"
    put root, "requirements.txt", "# c\nDjango==4.2.1\nrequests>=2.0\n-r other.txt\n"
    put root, "pyproject.toml", "[project]\ndependencies = [\n  \"fastapi>=0.100\",\n  \"pydantic\",\n]\n"
    put root, "README.md", "# Shop\n\nA [store](http://x) for **things**.\n#{'filler ' * 600}"
    put root, ".env", "DATABASE_URL=postgres://user:supersecretvalue@db/x\nPAYSTACK_KEY=sk_test_abc\n"
    put root, ".env.example", "REDIS_URL=\nexport MIXPANEL_TOKEN=\n"
    put root, "Dockerfile", "FROM scratch\n"
    put root, "docker-compose.yml", "services: {}\n"
    put root, ".github/workflows/ci.yml", "on: push\n"
    put root, "vercel.json", "{}"
    put root, "spec/foo_spec.rb", "describe 'x' do\nend\n"
    put root, "billing/plans.json", '{"plans":[{"name":"starter","price":29,"currency":"USD"},{"name":"pro","price":99}]}'
    put root, "billing/keys.go", "package billing\nvar k = \"AKIA" + "ABCDEFGHIJKLMNOP\"\nvar s = \"sk_" + "live_abcdefghijklmnopqrstuv\"\n"
    put root, "node_modules/leak/index.js", "app.get('/leak', h)\n"
    put root, "vendor/x/y.go", "package y\n"
    put root, ".git/config", "x"
    put root, "big.js", "// #{'a' * (1100 * 1024)}"
    put base, "outside/evil.js", "app.get('/escaped', h)\n"
    File.symlink(File.join(base, "outside"), File.join(root, "link"))
    File.symlink(File.join(base, "outside", "evil.js"), File.join(root, "evil-link.js"))
    root
  end

  def test_scan_fixture
    Dir.mktmpdir do |base|
      m = Linearjeph::Prophecy.scan(fixture(base))
      assert_equal 1, m["schema"]
      assert_equal "shop", m["root_name"]
      assert_equal 2, m["languages"]["go"]
      assert_equal 1, m["languages"]["js"] # big.js and the symlink are excluded
      assert_operator m["loc"], :>, 20
      routes = m["routes"].map { |r| "#{r['method']} #{r['path']}" }
      ["GET /health", "GET /api/v1/orders/:id", "POST /api/invoices", "GET /ping", "GET /posts/:post", "GET /photos",
       "DELETE /photos/:id", "GET /admin/users", "GET /admin/stats", "POST /login", "GET /express/items", "GET /dashboard",
       "ANY /articles/:pk/", "ANY /"].each { |w| assert_includes routes, w }
      refute(routes.any? { |r| r.include?("leak") || r.include?("escaped") })
      deps = m["dependencies"].to_h { |d| ["#{d['ecosystem']}:#{d['name']}", d["version"]] }
      { "go:github.com/go-chi/chi/v5" => "v5.0.10", "npm:express" => "^4.18.0", "npm:jest" => "29.0.0",
        "composer:laravel/framework" => "^10.0", "gem:rails" => "7.1.0", "pypi:django" => "4.2.1", "pypi:requests" => ">=2.0",
        "pypi:fastapi" => ">=0.100", "pypi:pydantic" => "" }.each { |k, v| assert_equal v, deps[k], k }
      refute deps.key?("go:golang.org/x/sys")
      refute deps.key?("composer:php")
      %w[chi express react laravel rails fastapi django].each { |f| assert_includes m["frameworks"], f }
      %w[stripe paystack sentry mixpanel redis].each { |i| assert_includes m["integrations"], i }
      %w[STRIPE_SECRET_KEY DATABASE_URL REDIS_URL MIXPANEL_TOKEN SENTRY_DSN FEATURE_DARK_MODE].each { |n| assert_includes m["env_names"], n }
      feat = m["features"].to_h { |f| [f["name"], f["source"]] }
      assert_equal "route", feat["orders"]
      assert_equal "flag", feat["beta-dash"]
      assert_equal "config", feat["dark_mode"]
      assert_equal "dir", feat["billing"]
      assert m["infra"]["docker"] && m["infra"]["compose"]
      assert_includes m["infra"]["ci"], "github-actions"
      assert_includes m["infra"]["hosting_hints"], "vercel"
      assert_equal 1, m["tests"]["files"]
      assert_includes m["tests"]["frameworks"], "rspec"
      assert_includes m["tests"]["frameworks"], "jest"
      assert m["security"]["lockfile"]
      assert m["security"]["env_committed"]
      assert_operator m["security"]["secrets_suspected"], :>=, 2
      assert_operator m["description"].size, :<=, 2048
      assert m["description"].start_with?("Shop A store for things")
      prices = m["pricing_hints"].to_h { |p| [p["plan"], p["amount"]] }
      assert_equal 29.0, prices["starter"]
      assert_equal 99.0, prices["pro"]
      json = JSON.generate(m)
      ["supersecretvalue", "postgres://", "AKIA" + "ABCDEFGHIJKLMNOP", "sk_live_abc", "sk_test_abc", "package main", "mux.HandleFunc"].each do |bad|
        refute_includes json, bad, "manifest leaks #{bad}"
      end
    end
  end

  def test_file_cap
    Dir.mktmpdir do |root|
      FileUtils.mkdir_p(File.join(root, "d"))
      20_050.times { |i| File.write(File.join(root, "d", "f#{i}.js"), "x\n") }
      assert_equal 20_000, Linearjeph::Prophecy.scan(root)["languages"]["js"]
    end
  end

  def test_missing_dir
    assert_raises(SystemCallError, Linearjeph::Prophecy::Error) { Linearjeph::Prophecy.scan("/nonexistent-dir-xyz") }
  end

  def test_cli
    Dir.mktmpdir do |base|
      csv = File.join(base, "e.csv")
      File.write(csv, "ts,uid,name,kind,amount,plan,source\n2024-01-01T00:00:00Z,u1,payment,payment,10.5,pro,web\n2024-01-02T00:00:00Z,u2,login,login,,,\n")
      rows = Linearjeph::Prophecy::Cli.load_rows(csv, "events")
      assert_equal 2, rows.size
      assert_equal 10.5, rows[0]["amount"]
      assert_equal({ "source" => "web" }, rows[0]["props"])
      out = StringIO.new
      assert_equal 0, Linearjeph::Prophecy::Cli.run(["scan", base], out, StringIO.new)
      assert_equal 1, JSON.parse(out.string)["schema"]
      assert_equal 2, Linearjeph::Prophecy::Cli.run([], out, StringIO.new)
    end
  end
end
