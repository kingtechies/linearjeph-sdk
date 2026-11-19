import gzip
import json
import logging
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

import linearjeph
from linearjeph import client, health, telemetry


class _H(BaseHTTPRequestHandler):
    got = []
    script = []

    def do_POST(self):
        raw = self.rfile.read(int(self.headers["Content-Length"]))
        enc = self.headers.get("Content-Encoding")
        body = json.loads(gzip.decompress(raw) if enc == "gzip" else raw)
        if _H.script:
            code, headers = _H.script.pop(0)
            self.send_response(code)
            for k, v in headers.items():
                self.send_header(k, v)
            self.end_headers()
            return
        _H.got.append((self.path, self.headers["X-LJ-Key"], body, enc))
        self.send_response(202)
        self.end_headers()
        self.wfile.write(b'{"accepted":1}')

    def log_message(self, *a):
        pass


@pytest.fixture
def server():
    _H.got, _H.script = [], []
    srv = HTTPServer(("127.0.0.1", 0), _H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{srv.server_port}"
    linearjeph.close(2)
    srv.shutdown()


def _init(url, **kw):
    kw.setdefault("backoff", 0.01)
    kw.setdefault("metrics_interval", 60)
    linearjeph.init("lj_app_testkey123", release="2.0.0", endpoint=url, hooks=False, **kw)


def _bodies(suffix, key):
    return [x for p, _, b, _ in _H.got if p.endswith(suffix) for x in b[key]]


def test_metrics_aggregate_and_flush(server):
    _init(server)
    for _ in range(5):
        linearjeph.incr("jobs.done", tags={"queue": "mail"})
    linearjeph.incr("jobs.done", 10, tags={"queue": "mail"})
    linearjeph.gauge("queue.depth", 3)
    linearjeph.gauge("queue.depth", 9)
    for v in (10, 20, 30):
        linearjeph.histogram("lat", v)
    with linearjeph.timer("block"):
        time.sleep(0.01)
    assert linearjeph.flush()
    m = {x["name"]: x for x in _bodies("/metrics", "metrics")}
    assert m["jobs.done"]["type"] == "counter" and m["jobs.done"]["value"] == 15 and m["jobs.done"]["tags"] == {"queue": "mail"}
    assert m["queue.depth"]["value"] == 9
    h = m["lat"]
    assert (h["count"], h["sum"], h["min"], h["max"]) == (3, 60, 10, 30) and sorted(h["values"]) == [10, 20, 30]
    assert m["block"]["type"] == "histogram" and m["block"]["min"] >= 9
    assert client._state["sender"].q.unfinished_tasks == 0


def test_metrics_are_sent_periodically_without_flush(server):
    _init(server, metrics_interval=1)
    linearjeph.incr("tick", 1)
    deadline = time.time() + 5
    while time.time() < deadline and not _bodies("/metrics", "metrics"):
        time.sleep(0.1)
    assert _bodies("/metrics", "metrics")[0]["name"] == "tick"


def test_metric_validation_series_cap_and_scrubbing(server):
    _init(server, max_metric_series=3)
    linearjeph.incr("bad name!")
    linearjeph.incr("ok", float("nan"))
    linearjeph.incr("ok", "x")  # swallowed
    for i in range(6):
        linearjeph.incr("many", tags={"i": str(i)})
    linearjeph.incr("p", tags={"password": "hunter2", "email": "a@b.co"})
    assert telemetry.dropped_metric_series() >= 3
    linearjeph.flush()
    ms = _bodies("/metrics", "metrics")
    assert len(ms) == 3 and not any(m["name"] in ("bad name!", "ok") for m in ms)
    assert linearjeph.incr("never", 1) is None


def test_metrics_gzip_for_large_batches(server):
    _init(server, max_metric_series=500)
    for i in range(300):
        linearjeph.incr("series.%d" % i, tags={"pad": "x" * 50})
    linearjeph.flush()
    assert any(enc == "gzip" for p, _, _, enc in _H.got if p.endswith("/metrics"))
    assert len(_bodies("/metrics", "metrics")) == 300


def test_never_raises_before_init_or_after_close(server):
    linearjeph.incr("a")
    linearjeph.gauge("a", 1)
    linearjeph.histogram("a", 1)
    linearjeph.log_event("x")
    linearjeph.checkin("job", "ok")
    with linearjeph.span("s"), linearjeph.timer("t"), linearjeph.monitor("m"):
        pass
    linearjeph.release("1")
    linearjeph.deploy("1")
    _init(server)
    linearjeph.close(2)
    linearjeph.incr("a")
    assert _H.got == []


def test_metric_queue_is_bounded(server):
    _H.script = [(500, {})] * 200  # server is failing: nothing drains quickly
    _init(server, max_queue=5, retries=0, max_metric_series=1000)
    s = client._state["sender"]
    for i in range(400):
        linearjeph.log_event("line %d" % i)
    assert s.q.qsize() <= 5 and s.dropped > 0


def test_scrub_pii_rules():
    assert linearjeph.scrub_pii("mail bob@example.com") == "mail [email]"
    assert linearjeph.scrub_pii("card 4111 1111 1111 1111 ok") == "card [card] ok"
    assert linearjeph.scrub_pii("order 1234567890123 id") == "order 1234567890123 id"  # not Luhn valid
    assert "[token]" in linearjeph.scrub_pii("jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig")
    assert linearjeph.scrub_pii("key lj_app_abcdefghijkl and sk-abcdefghijklmnopqrstuv") == "key [token] and [token]"
    assert "hunter2" not in linearjeph.scrub_pii("password=hunter2 Bearer abcdefghij1234")


def test_logs_structured_scrubbed_and_levels(server):
    _init(server, log_level="info")
    linearjeph.log_event("debug line", "debug")
    linearjeph.log_event("user ann@example.com paid with 4242424242424242", "error", logger="pay", order=5, password="x", note="a@b.co")
    assert linearjeph.flush()
    logs = _bodies("/logs", "logs")
    assert len(logs) == 1
    e = logs[0]
    assert e["level"] == "error" and e["logger"] == "pay" and e["message"] == "user [email] paid with [card]"
    assert e["context"] == {"order": 5, "password": "[Filtered]", "note": "[email]"} and e["release"] == "2.0.0"


def test_logs_can_skip_pii_scrubbing_and_use_hook(server):
    _init(server, pii_scrub=False, before_send_log=lambda it: None if "drop" in it["message"] else {**it, "context": {**it["context"], "hooked": True}})
    linearjeph.log_event("keep a@b.co")
    linearjeph.log_event("drop me")
    linearjeph.flush()
    logs = _bodies("/logs", "logs")
    assert [x["message"] for x in logs] == ["keep a@b.co"] and logs[0]["context"]["hooked"] is True


def test_logging_handler_integration(server):
    _init(server, logging_level=logging.WARNING)
    lg = logging.getLogger("shop.checkout")
    lg.setLevel(logging.DEBUG)
    lg.info("below the handler level")
    lg.warning("slow query %s", "users", extra={"duration_ms": 812, "table": "users"})
    try:
        1 / 0
    except ZeroDivisionError:
        lg.exception("crashed for ann@example.com")
    logging.getLogger("linearjeph").warning("own logs are never forwarded")
    assert linearjeph.flush()
    logs = _bodies("/logs", "logs")
    assert [x["level"] for x in logs] == ["warning", "error"]
    assert logs[0]["message"] == "slow query users" and logs[0]["context"] == {"duration_ms": 812, "table": "users"}
    assert "ZeroDivisionError" in logs[1]["message"] and "ann@example.com" not in logs[1]["message"]
    h = telemetry.install_logging(logging.ERROR)  # idempotent
    assert telemetry.install_logging(logging.ERROR) is h
    logging.getLogger().removeHandler(h)


def test_spans_nesting_ids_and_errors(server):
    _init(server)
    with linearjeph.span("handle", kind="server", tags={"route": "/x"}) as root:
        assert linearjeph.current_span() is root
        with linearjeph.span("db.query", kind="client"):
            pass
        with pytest.raises(KeyError):
            with linearjeph.span("boom"):
                raise KeyError("k")
        linearjeph.log_event("inside", "error")
    assert linearjeph.current_span() is None
    assert linearjeph.flush()
    sp = {s["name"]: s for s in _bodies("/spans", "spans")}
    assert set(sp) == {"handle", "db.query", "boom"} and len({s["trace_id"] for s in sp.values()}) == 1
    assert sp["handle"]["parent_id"] == "" and sp["db.query"]["parent_id"] == sp["handle"]["span_id"] and len(sp["handle"]["span_id"]) == 16
    assert sp["boom"]["status"] == "error" and sp["handle"]["status"] == "ok" and sp["db.query"]["kind"] == "client"
    assert sp["handle"]["tags"] == {"route": "/x"} and sp["handle"]["duration_ms"] >= sp["db.query"]["duration_ms"]
    lg = _bodies("/logs", "logs")[0]
    assert lg["trace_id"] == sp["handle"]["trace_id"] and lg["span_id"] == sp["handle"]["span_id"]  # logs join traces


def test_span_decorator_sampling_and_traceparent(server):
    _init(server, traces_sample_rate=0.0)
    with linearjeph.span("dropped"):
        pass
    _init(server, traces_sample_rate=1.0)

    @linearjeph.span("deco")
    def f():
        return linearjeph.inject_headers({})

    assert "traceparent" in f()
    tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
    with linearjeph.span("continued", kind="server", traceparent=tp):
        pass
    with linearjeph.span("not-sampled", traceparent="00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"):
        pass
    assert linearjeph.flush()
    sp = {s["name"]: s for s in _bodies("/spans", "spans")}
    assert set(sp) == {"deco", "continued"}
    assert sp["continued"]["trace_id"] == "0af7651916cd43dd8448eb211c80319c" and sp["continued"]["parent_id"] == "b7ad6b7169203331"
    assert telemetry.parse_traceparent("garbage") is None


def test_checkin_and_monitor_wrapper(server):
    _init(server)
    with linearjeph.monitor("nightly-backup", schedule="0 2 * * *", tz="Africa/Lagos", grace_s=600, max_runtime_s=3600):
        time.sleep(0.01)
    with pytest.raises(RuntimeError):
        with linearjeph.monitor("flaky"):
            raise RuntimeError("disk full for ann@example.com")

    @linearjeph.monitor("deco")
    def run():
        return 5

    assert run() == 5 and run() == 5
    linearjeph.checkin("manual", "start", run_id="r1")
    linearjeph.checkin("bad name!", "ok")
    assert linearjeph.flush()
    ci = [b for p, _, b, _ in _H.got if p.endswith("/checkin")]
    nb = [b for b in ci if b["monitor"] == "nightly-backup"]
    assert [b["status"] for b in nb] == ["start", "ok"] and nb[0]["run_id"] == nb[1]["run_id"]
    assert nb[1]["duration_ms"] >= 10 and nb[0]["schedule"] == "0 2 * * *" and nb[0]["tz"] == "Africa/Lagos" and nb[0]["grace_s"] == 600
    fl = [b for b in ci if b["monitor"] == "flaky"][1]
    assert fl["status"] == "fail" and "RuntimeError: disk full for [email]" == fl["message"]
    assert len([b for b in ci if b["monitor"] == "deco"]) == 4 and not any(b["monitor"] == "bad name!" for b in ci)
    assert ci[-1]["monitor"] == "manual" and ci[-1]["run_id"] == "r1"


def test_monitor_capture_reports_error_event(server):
    _init(server)
    with pytest.raises(ValueError):
        with linearjeph.monitor("cap", capture=True):
            raise ValueError("bad row")
    linearjeph.flush()
    assert any(e["exception"]["value"] == "bad row" for p, _, b, _ in _H.got if p.endswith("/events") for e in b["events"])


def test_release_and_deploy(server, monkeypatch):
    _init(server)
    linearjeph.release(commit="abc1234", notes="hotfix")
    linearjeph.deploy(status="success", url="https://ci.example/1", notify=True)
    linearjeph.deploy("2.0.1", environment="staging", status="failed")
    monkeypatch.setenv("LINEARJEPH_RELEASE", "env-1")
    linearjeph.flush()
    rel = [b for p, _, b, _ in _H.got if p.endswith("/releases")]
    dep = [b for p, _, b, _ in _H.got if p.endswith("/deploys")]
    assert rel == [{"version": "2.0.0", "commit": "abc1234", "notes": "hotfix"}]
    assert dep[0] == {"version": "2.0.0", "environment": "production", "status": "success", "notify": True, "url": "https://ci.example/1"}
    assert dep[1]["version"] == "2.0.1" and dep[1]["environment"] == "staging" and dep[1]["notify"] is False


def test_retry_429_then_delivered_and_401_disables(server):
    _H.script = [(429, {"Retry-After": "0"}), (503, {})]
    _init(server)
    linearjeph.incr("retry.me")
    assert linearjeph.flush(10)
    assert _bodies("/metrics", "metrics")[0]["name"] == "retry.me"
    _H.script = [(401, {})]
    linearjeph.log_event("a")
    linearjeph.flush(3)
    assert client._state["sender"].disabled
    linearjeph.log_event("b")  # no more sends after a bad key
    linearjeph.flush(1)
    assert _bodies("/logs", "logs") == []


def test_413_splits_logs_batch(server):
    _H.script = [(413, {})]
    _init(server, batch_size=50)
    s = client._state["sender"]
    gate = threading.Event()
    orig = s._send_batch
    s._send_batch = lambda p, items: (gate.wait(2), orig(p, items))
    for i in range(4):
        linearjeph.log_event("l%d" % i)
    gate.set()
    assert linearjeph.flush(5)
    assert sorted(x["message"] for x in _bodies("/logs", "logs")) == ["l0", "l1", "l2", "l3"]


# ---------------------------------------------------------------- health checks

@pytest.fixture
def clean_health():
    health._checks.clear()
    yield
    health._checks.clear()


def test_health_registry_statuses_and_thresholds(clean_health):
    health.register("db", lambda: 50.0, warn=200, fail=1000)
    health.register("slow_db", lambda: 450, warn=200, fail=1000)
    health.register("dead_db", lambda: 5000, warn=200, fail=1000)
    health.register("disk_free", lambda: 3, warn=10, fail=5, below=True)
    health.register("cache", lambda: True)
    health.register("down", lambda: False)
    health.register("boom", lambda: 1 / 0)
    health.register("rich", lambda: {"status": "warn", "value": 7, "message": "replica lag"})
    health.register("hang", lambda: time.sleep(2), timeout=0.2)
    body = health.run_checks()
    c = body["checks"]
    assert {k: v["status"] for k, v in c.items()} == {"db": "pass", "slow_db": "warn", "dead_db": "fail", "disk_free": "fail", "cache": "pass",
                                                      "down": "fail", "boom": "fail", "rich": "warn", "hang": "fail"}
    assert body["status"] == "fail" and c["db"]["value"] == 50.0 and "ZeroDivisionError" in c["boom"]["message"] and "timed out" in c["hang"]["message"]
    code, _ = health.respond()
    assert code == 503


def test_health_ok_degraded_and_token(clean_health):
    health.register("a", lambda: True)
    assert health.respond()[0] == 200 and health.respond()[1]["status"] == "ok"
    health.register("b", lambda: 300, warn=100)
    code, body = health.respond()
    assert code == 200 and body["status"] == "degraded"
    assert health.respond(None, "s3cret")[0] == 401 and health.respond("Bearer wrong", "s3cret")[0] == 401
    assert health.respond("Bearer s3cret", "s3cret")[0] == 200


def test_health_wsgi_and_many_checks_do_not_deadlock(clean_health):
    for i in range(12):
        health.register("c%d" % i, lambda: True)
    app = health.wsgi_app(token="t")
    out = {}
    res = app({"HTTP_AUTHORIZATION": "Bearer t"}, lambda status, headers: out.update(status=status, headers=dict(headers)))
    body = json.loads(b"".join(res))
    assert out["status"].startswith("200") and len(body["checks"]) == 12 and out["headers"]["Cache-Control"] == "no-store"
    app({}, lambda status, headers: out.update(status=status))
    assert out["status"].startswith("401")
    health.unregister("c0")
    assert "c0" not in health.run_checks()["checks"]


# ---------------------------------------------------------------- middleware integration

def test_django_middleware_sends_span_with_traceparent(server):
    import types
    from linearjeph.integrations.django import LinearjephMiddleware
    _init(server)
    mw = LinearjephMiddleware(lambda r: types.SimpleNamespace(status_code=500))
    req = types.SimpleNamespace(method="GET", path="/items/5", META={"HTTP_TRACEPARENT": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
                                resolver_match=types.SimpleNamespace(route="items/<int:id>"))
    mw(req)
    assert linearjeph.flush()
    sp = _bodies("/spans", "spans")[0]
    assert sp["name"] == "GET items/<int:id>" and sp["kind"] == "server" and sp["status"] == "error" and sp["trace_id"] == "0af7651916cd43dd8448eb211c80319c"
    txn = [e for p, _, b, _ in _H.got if p.endswith("/events") for e in b["events"]]
    assert txn[0]["transaction"]["name"] == "GET /items/<int:id>"


def test_version():
    assert linearjeph.__version__ == "1.2.0"
