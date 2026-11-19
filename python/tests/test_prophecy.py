import asyncio
import gzip
import hashlib
import json
import os
import sys
import threading
import types
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

import linearjeph
from linearjeph import prophecy
from linearjeph.prophecy import __main__ as cli
from linearjeph.prophecy import middleware as mw
from linearjeph.prophecy.django import ProphecyMiddleware


def sha(s):
    return hashlib.sha256(str(s).encode()).hexdigest()


class _H(BaseHTTPRequestHandler):
    got, hits, script = [], [], []

    def _handle(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        gz = self.headers.get("Content-Encoding") == "gzip"
        if gz:
            raw = gzip.decompress(raw)
        body = json.loads(raw) if raw else None
        if _H.script:
            code, headers = _H.script.pop(0)
            _H.hits.append(code)
            self.send_response(code)
            for k, v in headers.items():
                self.send_header(k, v)
            self.send_header("Content-Length", "2")
            self.end_headers()
            self.wfile.write(b"{}")
            return
        _H.hits.append(200)
        _H.got.append({"method": self.command, "path": self.path, "headers": {k.lower(): v for k, v in self.headers.items()}, "gz": gz, "body": body})
        out = {"accepted": 1, "ok": True}
        if self.path.startswith("/api/v1/prophecy/sdk/import"):
            out = {"job": len(_H.got), "accepted": len(body["rows"]), "rejected": 0, "errors": []}
        elif self.path.startswith("/api/v1/prophecy/sdk/embed"):
            out = {"url": "https://x/prophecy-embed/tok", "expires_at": "z"}
        data = json.dumps(out).encode()
        self.send_response(202 if self.path.startswith("/ingest/") else 200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    do_POST = do_GET = _handle

    def log_message(self, *a):
        pass


@pytest.fixture
def server():
    _H.got, _H.hits, _H.script = [], [], []
    srv = HTTPServer(("127.0.0.1", 0), _H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    url = "http://127.0.0.1:%d" % srv.server_port
    prophecy.init(ingest_key="lj_app_k", secret_key="ljp_sk_" + "a" * 32, endpoint=url, backoff=0.005, hooks=False)
    yield url
    prophecy.close(2)
    srv.shutdown()


def events():
    return [e for g in _H.got if g["body"] and "events" in g["body"] for e in g["body"]["events"]]


def test_exposed_on_package():
    assert linearjeph.prophecy is prophecy and prophecy.SDK_VERSION == "1.2.0"
    for f in ("init", "track", "feature", "signup", "payment", "subscription", "churn", "identify", "scan", "send_manifest",
              "import_history", "report", "refresh", "live", "embed_url", "flush", "close"):
        assert callable(getattr(prophecy, f)), f


def test_track_batches_gzip_hash(server):
    prophecy.track("exported_report", uid="user-1", props={"rows": 3})
    prophecy.feature("search", "user-1")
    prophecy.signup("user-2", plan="pro", channel="ads", country="NG")
    prophecy.payment("user-2", 29, "USD", plan="pro")
    prophecy.payment("user-2", -29)
    prophecy.subscription("user-2", plan="pro", mrr=29, status="active")
    prophecy.churn("user-2", "too expensive")
    prophecy.identify("user-3", {"plan": "free", "country": "GH", "shoe_size": 9})
    assert prophecy.flush()
    e = events()
    assert len(e) == 7
    post = [g for g in _H.got if g["path"] == "/ingest/v1/prophecy/events"][0]
    assert post["gz"] and post["headers"]["x-lj-key"] == "lj_app_k"
    assert e[0]["uid"] == sha("user-1") and e[0]["kind"] == "custom" and e[0]["props"] == {"rows": 3}
    assert e[1]["kind"] == "feature" and e[1]["feature"] == "search"
    assert e[3]["kind"] == "payment" and e[3]["amount"] == 29 and e[3]["currency"] == "USD"
    assert e[4]["kind"] == "refund" and e[4]["amount"] == -29
    assert e[5]["kind"] == "subscription" and e[5]["props"]["mrr"] == 29
    assert e[6]["kind"] == "churn"
    custs = [c for g in _H.got if g["path"] == "/ingest/v1/prophecy/identify" for c in g["body"]["customers"]]
    c3 = [c for c in custs if c["uid"] == sha("user-3")][0]
    assert c3["plan"] == "free" and c3["traits"] == {"shoe_size": 9}
    assert any(c["uid"] == sha("user-2") and c.get("signup_at") and c["country"] == "NG" for c in custs)
    assert "user-1" not in json.dumps(_H.got)


def test_hash_off(server):
    prophecy.init(ingest_key="k", endpoint=server, hash_user_ids=False, hooks=False)
    prophecy.track("x", uid="raw-id")
    prophecy.flush()
    assert events()[0]["uid"] == "raw-id"


def test_batches_of_100(server):
    for i in range(250):
        prophecy.track("e%d" % i)
    prophecy.flush()
    assert sorted(len(g["body"]["events"]) for g in _H.got if "events" in g["body"]) == [50, 100, 100]


def test_queue_bounded(monkeypatch):
    release = threading.Event()
    sent = []
    monkeypatch.setattr(prophecy, "_post_ingest", lambda path, body, last_chance=False: (release.wait(5), sent.append(len(body["events"])))[1])
    prophecy.init(ingest_key="k", endpoint="http://127.0.0.1:9", hooks=False, retries=0, flush_interval=60)
    for _ in range(10500):
        prophecy.track("e")
    assert len(prophecy._st["queue"]) <= 10000 and prophecy._st["dropped"] >= 400
    prophecy._st["queue"].clear()
    release.set()
    prophecy.close(2)


def test_retry_503_then_ok_and_401_disables(server):
    _H.script = [(503, {}), (429, {"Retry-After": "0"})]
    prophecy.track("x")
    assert prophecy.flush()
    assert _H.hits == [503, 429, 200]
    _H.script, _H.hits = [(401, {})], []
    prophecy.track("a")
    prophecy.flush()
    prophecy.track("b")
    prophecy.flush()
    assert _H.hits == [401]


def test_never_raises(caplog):
    prophecy.init(ingest_key="k", endpoint="http://127.0.0.1:9", hooks=False, retries=0, flush_interval=60)
    circ = {}
    circ["self"] = circ
    prophecy.track("x", props=circ)
    prophecy.track(None, kind=object())
    prophecy.payment(None, "abc")
    prophecy.signup(None)
    prophecy.identify(None)
    prophecy.subscription("u", None, "x")
    prophecy.feature(None)
    prophecy._st["queue"].clear()
    prophecy.close(1)
    prophecy.init()  # no keys: calls are no-ops
    prophecy.track("x")


def test_secret_key_refused_as_ingest_key():
    assert prophecy.init(ingest_key="ljp_sk_" + "b" * 32) is False


def test_secret_calls(server):
    prophecy.report("revenue")
    prophecy.refresh()
    prophecy.live()
    assert prophecy.embed_url(999999) == "https://x/prophecy-embed/tok"
    r, f, l, e = _H.got
    assert r["method"] == "GET" and r["path"] == "/api/v1/prophecy/sdk/report?section=revenue"
    assert r["headers"]["authorization"] == "Bearer ljp_sk_" + "a" * 32
    assert f["method"] == "POST" and f["path"] == "/api/v1/prophecy/sdk/refresh"
    assert l["path"] == "/api/v1/prophecy/sdk/live" and e["body"]["ttl"] == 86400
    prophecy.init(ingest_key="k", endpoint=server, hooks=False)
    with pytest.raises(prophecy.ProphecyError, match="secret_key missing"):
        prophecy.report()


def test_import_history_batches_retry_progress(server):
    _H.script = [(503, {}), (429, {"Retry-After": "0"})]
    rows = [{"ts": "2025-01-01T00:00:00Z", "uid": "u%d" % i, "name": "x", "amount": "9.5" if i % 2 else "", "id": "r%d" % i} for i in range(12000)]
    prog = []
    r = prophecy.import_history("events", rows, on_progress=lambda p: prog.append(p["done"]))
    assert _H.hits[:3] == [503, 429, 200]
    assert [len(g["body"]["rows"]) for g in _H.got] == [5000, 5000, 2000]
    assert prog == [5000, 10000, 12000] and r["accepted"] == 12000
    assert _H.got[0]["body"]["rows"][0]["uid"] == sha("u0") and _H.got[0]["body"]["rows"][1]["amount"] == 9.5
    assert "amount" not in _H.got[0]["body"]["rows"][0]
    with pytest.raises(prophecy.ProphecyError):
        prophecy.import_history("nope", [])


def test_import_4xx_not_retried(server):
    _H.script = [(402, {})]
    with pytest.raises(prophecy.ProphecyError, match="402"):
        prophecy.import_history("customers", [{"uid": "a"}])
    assert _H.hits == [402]


# ------------------------------------------------------------------ middlewares
def test_normalize_and_templates():
    assert mw.normalize_path("/orders/7f9c2c0e-1b2a-4c3d-8e9f-0a1b2c3d4e5f/items/12345?x=1") == "/orders/:id/items/:id"
    assert mw.normalize_path("/about") == "/about"
    assert mw.route_template("users/<int:pk>/") == "/users/:pk/"
    assert mw.route_template("/items/{item_id}") == "/items/:item_id"
    assert mw.route_template("^orders/(?P<id>[0-9]+)/$") == "/orders/:id/"


def test_wsgi_middleware(server):
    def app(environ, start_response):
        start_response("404 Not Found", [("Content-Type", "text/plain")])
        return [b"nope"]

    wrapped = mw.WSGIMiddleware(app, ignore_paths=("/health",))
    for path, extra in (("/orders/12345/items", {}), ("/health", {}), ("/users/9", {"prophecy.route": "/users/:id"})):
        env = {"REQUEST_METHOD": "GET", "PATH_INFO": path, "QUERY_STRING": "token=secret", "HTTP_AUTHORIZATION": "Bearer x"}
        env.update(extra)
        it = wrapped(env, lambda s, h, e=None: None)
        assert list(it) == [b"nope"]
        it.close()
    prophecy.flush()
    e = events()
    assert [x["props"]["route"] for x in e] == ["/orders/:id/items", "/users/:id"]
    assert e[0]["kind"] == "request" and e[0]["props"]["status"] == 404 and e[0]["props"]["method"] == "GET"
    assert isinstance(e[0]["props"]["duration_ms"], float)
    wire = json.dumps(_H.got)
    assert "token=secret" not in wire and "12345" not in wire and "Bearer" not in wire


def test_asgi_middleware(server):
    async def app(scope, receive, send):
        scope["route"] = types.SimpleNamespace(path_format="/items/{item_id}")
        await send({"type": "http.response.start", "status": 201, "headers": []})
        await send({"type": "http.response.body", "body": b"{}"})

    async def run(scope, a):
        sent = []

        async def send(m):
            sent.append(m)

        async def receive():
            return {"type": "http.request"}

        await mw.ASGIMiddleware(a, ignore_paths=("/health",))(scope, receive, send)
        return sent

    asyncio.run(run({"type": "http", "method": "POST", "path": "/items/77", "query_string": b"a=1"}, app))
    asyncio.run(run({"type": "http", "method": "GET", "path": "/health"}, app))

    async def noroute(scope, receive, send):
        await send({"type": "http.response.start", "status": 200, "headers": []})

    asyncio.run(run({"type": "http", "method": "GET", "path": "/files/123456"}, noroute))
    asyncio.run(run({"type": "websocket", "path": "/ws"}, noroute))
    prophecy.flush()
    e = events()
    assert [(x["props"]["route"], x["props"]["status"], x["props"]["method"]) for x in e] == [("/items/:item_id", 201, "POST"), ("/files/:id", 200, "GET")]


def test_flask_helper(server, monkeypatch):
    state = types.SimpleNamespace()
    flask = types.ModuleType("flask")
    flask.g = types.SimpleNamespace()
    flask.request = types.SimpleNamespace(path="/u/5", method="PUT", url_rule=types.SimpleNamespace(rule="/u/<int:uid>"))
    monkeypatch.setitem(sys.modules, "flask", flask)

    class App:
        def before_request(self, f):
            state.before = f
            return f

        def after_request(self, f):
            state.after = f
            return f

    mw.init_flask(App())
    state.before()
    state.after(types.SimpleNamespace(status_code=200))
    prophecy.flush()
    assert events()[0]["props"] == {"route": "/u/:uid", "method": "PUT", "status": 200, "duration_ms": events()[0]["props"]["duration_ms"]}


def test_django_middleware(server):
    resp = types.SimpleNamespace(status_code=200)
    m = ProphecyMiddleware(lambda r: resp)
    req = types.SimpleNamespace(path="/blog/42/", method="GET", resolver_match=types.SimpleNamespace(route="blog/<int:pk>/"),
                                user=types.SimpleNamespace(is_authenticated=True, pk=7), META={"HTTP_COOKIE": "s=1"})
    assert m(req) is resp

    async def aget(r):
        return types.SimpleNamespace(status_code=404)

    am = ProphecyMiddleware(aget)
    assert asyncio.iscoroutinefunction(am)
    asyncio.run(am(types.SimpleNamespace(path="/x/99999", method="GET", resolver_match=None)))
    prophecy.flush()
    e = events()
    assert e[0]["props"]["route"] == "/blog/:pk/" and e[0]["uid"] == sha(7)
    assert e[1]["props"]["route"] == "/x/:id" and e[1]["props"]["status"] == 404


# ------------------------------------------------------------------ scanner
def w(root, rel, text):
    p = os.path.join(root, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f:
        f.write(text)


AKIA = "AKIA" + "ABCDEFGHIJKLMNOP"
SK = "sk_live_" + "x" * 24


@pytest.fixture
def tree(tmp_path):
    r = str(tmp_path / "shop")
    w(r, "package.json", json.dumps({"name": "shop", "dependencies": {"express": "^4.18.0", "next": "14.0.0", "stripe": "^14.0.0", "react": "18.2.0"},
                                     "devDependencies": {"jest": "^29.0.0"}}))
    w(r, "package-lock.json", "{}")
    w(r, "README.md", "# Shop\n\nA **great** [shop](http://x.y) for shoes.\n\n```js\nconst k = '%s';\n```\n" % AKIA)
    w(r, "server.js", 'const app = require("express")();\napp.get("/api/users/:id", h);\napp.post(\'/billing/charge\', h);\n'
                      'const k = process.env.STRIPE_SECRET_KEY; const d = process.env["DATABASE_URL"];\n'
                      'if (flags.betaSearch && isEnabled("new-checkout")) {}\nconst key = "%s"; const t = "%s";\n' % (AKIA, SK))
    w(r, "app/api/orders/route.ts", "export async function GET() {}\nexport async function POST() {}\n")
    w(r, "app/(shop)/products/[slug]/page.tsx", "export default function P() {}\n")
    w(r, "pages/about.tsx", "export default function A() {}\n")
    w(r, "pages/api/ping.ts", "export default function h() {}\n")
    w(r, "billing/plans.js", "const plans = { pro: { price: 29, currency: 'usd' } };\n")
    w(r, "api/urls.py", "from django.urls import path\nurlpatterns = [path('invoices/<int:pk>/', v), path('health/', v)]\n")
    w(r, "api/app.py", "import os\n@app.route('/items', methods=['GET', 'POST'])\ndef a(): os.environ['PY_SECRET']\n@app.get('/fast/{id}')\ndef b(): pass\n")
    w(r, "routes/web.php", "<?php\nRoute::get('/dashboard', fn() => 1);\n")
    w(r, "config/routes.rb", "get '/ping', to: 'x#y'\nresources :widgets\n")
    w(r, "main.go", 'package main\nfunc main(){ http.HandleFunc("/healthz", h); r.Get("/v1/things", h) }\n')
    w(r, "requirements.txt", "flask==2.3.1\nrequests>=2.0\n# c\n")
    w(r, "go.mod", "module x\nrequire (\n\tgithub.com/gin-gonic/gin v1.9.1\n)\n")
    w(r, "composer.json", json.dumps({"require": {"php": ">=8", "laravel/framework": "^10.0"}}))
    w(r, "Gemfile", "gem 'rails', '~> 7.0'\n")
    w(r, ".env", "SECRET_VALUE_SHOULD_NOT_BE_READ=hunter2\n")
    w(r, ".env.example", "PORT=3000\nSTRIPE_SECRET_KEY=sk_test_placeholder\n# comment\nexport MAIL_HOST=smtp\n")
    w(r, "Dockerfile", "FROM node\n")
    w(r, "docker-compose.yml", "services: {}\n")
    w(r, "k8s/deploy.yaml", "apiVersion: apps/v1\nkind: Deployment\n")
    w(r, "infra/main.tf", "resource {}\n")
    w(r, ".github/workflows/ci.yml", "on: push\n")
    w(r, "vercel.json", "{}")
    w(r, "tests/test_a.py", "import pytest\ndef test_x(): pass\n")
    w(r, "node_modules/evil/index.js", 'app.get("/should/not/appear", h);\nconst k = "%s";\n' % AKIA)
    w(r, "dist/out.js", 'app.get("/dist/no", h);\n')
    w(r, "big.js", "x" * (1024 * 1024 + 10))
    return r


def test_scanner_manifest(tree):
    m = prophecy.scan(tree)
    assert m["schema"] == 1 and m["root_name"] == "shop"
    routes = {r["method"] + " " + r["path"] for r in m["routes"]}
    for want in ("GET /api/users/:id", "POST /billing/charge", "GET /api/orders", "POST /api/orders", "GET /products/:slug", "GET /about",
                 "GET /api/ping", "ANY /invoices/:pk/", "GET /items", "POST /items", "GET /fast/{id}", "GET /dashboard", "GET /ping",
                 "GET /widgets", "DELETE /widgets/:id", "ANY /healthz", "GET /v1/things"):
        assert want in routes, (want, sorted(routes))
    assert not any("should/not" in r or "dist/no" in r for r in routes)
    assert m["languages"]["js"] >= 2 and m["languages"]["py"] == 3 and m["loc"] > 20 and "md" not in m["languages"]
    assert {"express", "next", "laravel", "gin"} <= set(m["frameworks"])
    dep = {(d["ecosystem"], d["name"]): d["version"] for d in m["dependencies"]}
    assert dep[("npm", "express")] == "^4.18.0" and dep[("pypi", "flask")] == "2.3.1" and dep[("go", "github.com/gin-gonic/gin")] == "v1.9.1"
    assert ("composer", "laravel/framework") in dep and ("composer", "php") not in dep and ("gem", "rails") in dep
    assert "stripe" in m["integrations"]
    for n in ("PORT", "STRIPE_SECRET_KEY", "MAIL_HOST", "DATABASE_URL", "PY_SECRET"):
        assert n in m["env_names"], n
    assert "SECRET_VALUE_SHOULD_NOT_BE_READ" not in m["env_names"]
    flags = {f["name"] for f in m["features"] if f["source"] == "flag"}
    assert {"new-checkout", "betaSearch"} <= flags
    assert any(f["source"] == "route" and f["name"] == "billing" for f in m["features"]) and any(f["source"] == "dir" for f in m["features"])
    i = m["infra"]
    assert i["docker"] and i["compose"] and i["kubernetes"] and i["terraform"] and i["ci"] == ["github-actions"] and i["hosting_hints"] == ["vercel"]
    assert m["security"] == {"lockfile": True, "env_committed": True, "secrets_suspected": 3}
    assert any(p["plan"] == "pro" and p["amount"] == 29 and p["currency"] == "USD" for p in m["pricing_hints"])
    assert m["description"].startswith("Shop A great shop for shoes.") and "AKIA" not in m["description"]
    assert m["tests"]["files"] == 1 and {"jest", "pytest"} <= set(m["tests"]["frameworks"])
    wire = json.dumps(m)
    for leak in (AKIA, SK, "hunter2", "sk_test_placeholder", "app.get("):
        assert leak not in wire, leak


def test_env_gitignored_and_symlinks(tree, tmp_path):
    w(tree, ".gitignore", "node_modules\n.env\n")
    outside = str(tmp_path / "outside")
    w(outside, "leak.js", 'app.get("/outside/route", h);\n')
    try:
        os.symlink(outside, os.path.join(tree, "linked"))
        os.symlink(os.path.join(outside, "leak.js"), os.path.join(tree, "leak.js"))
    except OSError:
        pass
    m = prophecy.scan(tree)
    assert m["security"]["env_committed"] is False
    assert not any(r["path"] == "/outside/route" for r in m["routes"])


def test_file_cap_and_speed(tmp_path):
    import time
    r = str(tmp_path / "big")
    for d in range(20):
        for f in range(60):
            w(r, "d%d/f%d.js" % (d, f), 'app.get("/r%d/%d", h);\n' % (d, f))
    t0 = time.time()
    m = prophecy.scan(r, max_files=500)
    assert time.time() - t0 < 5 and 100 < len(m["routes"]) <= 500
    assert len(prophecy.scan(r)["routes"]) == 1200


def test_send_manifest_trims(server, tree):
    m = prophecy.scan(tree)
    m["routes"] = [{"method": "GET", "path": "/some/long/route/number/%d/%s" % (i, "x" * 60), "file": "f" * 60} for i in range(6000)]
    assert prophecy.send_manifest(m) is True
    g = _H.got[0]
    assert g["path"] == "/ingest/v1/prophecy/manifest" and g["gz"] and g["body"]["manifest"]["schema"] == 1
    assert len(json.dumps(g["body"])) <= 512 * 1024 and len(g["body"]["manifest"]["routes"]) < 6000


def test_cli_scan_and_import(server, tree, tmp_path, capsys, monkeypatch):
    monkeypatch.setenv("LINEARJEPH_INGEST_KEY", "lj_app_k")
    monkeypatch.setenv("LINEARJEPH_SECRET_KEY", "ljp_sk_" + "z" * 32)
    monkeypatch.setenv("LINEARJEPH_ENDPOINT", server)
    assert cli.main(["scan", tree, "--send"]) == 0
    assert _H.got[0]["path"] == "/ingest/v1/prophecy/manifest"
    csvf = str(tmp_path / "e.csv")
    with open(csvf, "w") as f:
        f.write('ts,uid,name,kind,feature,amount,currency\n2025-01-01T00:00:00Z,"a,b",paid,payment,,12.5,USD\n')
    assert cli.main(["import", "events", csvf]) == 0
    row = _H.got[-1]["body"]["rows"][0]
    assert row["uid"] == sha("a,b") and row["amount"] == 12.5 and "feature" not in row
    assert cli.main([]) == 2
