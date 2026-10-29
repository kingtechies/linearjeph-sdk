import asyncio
import json
import queue
import sys
import threading
import time
import types
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

import linearjeph
from linearjeph import client


class _H(BaseHTTPRequestHandler):
    got = []
    script = []  # list of (status, headers) consumed first, then 202

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if _H.script:
            code, headers = _H.script.pop(0)
            _H.hits.append(code)
            self.send_response(code)
            for k, v in headers.items():
                self.send_header(k, v)
            self.end_headers()
            return
        _H.hits.append(202)
        _H.got.append((self.path, self.headers["X-LJ-Key"], body))
        self.send_response(202)
        self.end_headers()
        self.wfile.write(b'{"accepted":1}')

    def log_message(self, *a):
        pass


_H.hits = []


@pytest.fixture
def server():
    _H.got, _H.script, _H.hits = [], [], []
    srv = HTTPServer(("127.0.0.1", 0), _H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{srv.server_port}"
    linearjeph.close(2)
    srv.shutdown()


def _init(url, **kw):
    kw.setdefault("backoff", 0.01)
    linearjeph.init("lj_app_testkey123", release="1.0.0", endpoint=url, hooks=False, **kw)


def _events():
    return [e for _, _, b in _H.got if "events" in b for e in b["events"]]


def test_capture_exception_payload(server):
    _init(server)
    try:
        1 / 0
    except ZeroDivisionError:
        linearjeph.capture_exception()
    assert linearjeph.flush()
    path, key, body = _H.got[0]
    ev = body["events"][0]
    assert path == "/ingest/v1/events" and key == "lj_app_testkey123"
    assert ev["type"] == "error" and ev["release"] == "1.0.0" and ev["environment"] == "production"
    assert ev["exception"]["type"] == "ZeroDivisionError"
    fr = ev["exception"]["stacktrace"][-1]
    assert set(fr) == {"filename", "function", "lineno", "in_app"} and fr["function"] == "test_capture_exception_payload"


def test_retry_on_503_then_success(server):
    _H.script = [(503, {}), (503, {})]
    _init(server)
    linearjeph.capture_message("hello")
    assert linearjeph.flush()
    assert len(_H.got) == 1 and _H.got[0][2]["events"][0]["exception"]["value"] == "hello"
    assert _H.hits == [503, 503, 202]


def test_429_honours_retry_after_then_succeeds(server):
    _H.script = [(429, {"Retry-After": "0"})]
    _init(server)
    linearjeph.capture_message("later")
    assert linearjeph.flush()
    assert _H.hits == [429, 202]


def test_401_disables_sending_and_never_retries(server):
    _H.script = [(401, {})]
    _init(server)
    linearjeph.capture_message("a")
    assert linearjeph.flush()
    linearjeph.capture_message("b")
    time.sleep(0.1)
    assert _H.hits == [401] and client._state["sender"].disabled


def test_413_and_400_drop_without_retry(server):
    _H.script = [(413, {}), (400, {})]
    _init(server)
    linearjeph.capture_message("a")
    assert linearjeph.flush()
    linearjeph.capture_message("b")
    assert linearjeph.flush()
    assert _H.hits == [413, 400] and _H.got == []


def test_gives_up_after_retries_and_does_not_raise(server):
    _H.script = [(500, {})] * 10
    _init(server, retries=2)
    linearjeph.capture_message("x")
    assert linearjeph.flush()
    assert _H.hits == [500, 500, 500]


def test_unreachable_endpoint_never_raises():
    linearjeph.init("lj_app_testkey123", endpoint="http://127.0.0.1:9", hooks=False, retries=1, backoff=0.01)
    linearjeph.capture_message("nobody home")
    assert linearjeph.flush(5)
    linearjeph.close(1)


def test_bounded_queue_drops_instead_of_blocking():
    s = client.Sender(client.Options("k", max_queue=2))
    s.q = queue.Queue(maxsize=2)
    s._ensure_thread = lambda: None  # no consumer: the queue must fill
    for _ in range(5):
        s.put("/x", {})
    assert s.q.qsize() == 2 and s.dropped == 3


def test_events_are_batched(server):
    _init(server, batch_size=50)
    s = client._state["sender"]
    gate = threading.Event()
    orig = s._send_events
    s._send_events = lambda evs: (gate.wait(2), orig(evs))
    for i in range(10):
        linearjeph.capture_message("m%d" % i)
    gate.set()
    assert linearjeph.flush()
    assert len(_events()) == 10 and len(_H.got) < 10


def test_heartbeat_and_packages(server):
    _init(server)
    linearjeph.heartbeat("nightly", "ok", interval_s=3600)
    n = linearjeph.report_packages()
    assert linearjeph.flush() and n > 0
    by = {p: b for p, _, b in _H.got}
    assert by["/ingest/v1/heartbeat"] == {"name": "nightly", "status": "ok", "interval_s": 3600}
    pk = by["/ingest/v1/packages"]
    assert pk["ecosystem"] == "PyPI" and any(p["name"] == "pytest" for p in pk["packages"])
    assert len(json.dumps(pk)) < 256 * 1024


def test_heartbeat_validation(server):
    _init(server)
    linearjeph.heartbeat("bad name!", "ok")
    linearjeph.heartbeat("x" * 65, "ok")
    linearjeph.heartbeat("job", "weird", interval_s=5)
    assert linearjeph.flush()
    assert [b for _, _, b in _H.got] == [{"name": "job", "status": "error", "interval_s": 30}]


def test_monitor_context_and_decorator(server):
    _init(server)
    with linearjeph.monitor("ctx", interval_s=60):
        pass
    with pytest.raises(ValueError):
        with linearjeph.monitor("ctx-fail", capture=True):
            raise ValueError("nope")

    @linearjeph.monitor("deco", interval_s=120)
    def job():
        return 7

    assert job() == 7
    assert linearjeph.flush()
    hb = {b["name"]: b for p, _, b in _H.got if p.endswith("heartbeat")}
    assert hb["ctx"]["status"] == "ok" and hb["ctx-fail"]["status"] == "error" and hb["deco"]["interval_s"] == 120
    assert any(e["exception"]["value"] == "nope" for e in _events())


def test_start_transaction_context_and_decorator(server):
    _init(server)
    with linearjeph.start_transaction("block"):
        time.sleep(0.01)
    with pytest.raises(KeyError):
        with linearjeph.start_transaction("bad"):
            raise KeyError("k")

    @linearjeph.transaction("decorated")
    def f():
        return 1

    f(), f()
    assert linearjeph.flush()
    tx = [e["transaction"] for e in _events() if e["type"] == "transaction"]
    names = [t["name"] for t in tx]
    assert names.count("decorated") == 2 and "block" in names
    assert next(t for t in tx if t["name"] == "bad")["status"] == "error"
    assert next(t for t in tx if t["name"] == "block")["duration_ms"] >= 10


def test_scrubbing_of_tags_and_text(server):
    _init(server, scrub_keys=["ssn"])
    linearjeph.set_tag("password", "hunter2")
    linearjeph.set_tag("Authorization", "Bearer abcdefghijkl")
    linearjeph.set_tag("ssn", "123")
    linearjeph.set_tag("region", "eu")
    linearjeph.capture_message("login failed password=hunter2 for user; Bearer abcdefghijklmnop", tags={"api_key": "k"})
    assert linearjeph.flush()
    ev = _events()[0]
    assert ev["tags"]["password"] == ev["tags"]["Authorization"] == ev["tags"]["ssn"] == ev["tags"]["api_key"] == "[Filtered]"
    assert ev["tags"]["region"] == "eu"
    assert "hunter2" not in json.dumps(ev) and "abcdefghijklmnop" not in json.dumps(ev)


def test_scrub_helper_nested():
    out = client.scrub({"headers": {"Cookie": "a=b", "X-Ok": "1"}, "items": [{"token": "t", "n": 1}]})
    assert out == {"headers": {"Cookie": "[Filtered]", "X-Ok": "1"}, "items": [{"token": "[Filtered]", "n": 1}]}


def test_before_send_can_modify_and_drop(server):
    def hook(ev):
        if ev["type"] == "transaction":
            return None
        ev["tags"]["seen"] = "yes"
        return ev
    _init(server, before_send=hook)
    linearjeph.capture_transaction("t", 1)
    linearjeph.capture_message("kept")
    assert linearjeph.flush()
    evs = _events()
    assert len(evs) == 1 and evs[0]["tags"]["seen"] == "yes"


def test_before_send_exception_drops_event(server):
    _init(server, before_send=lambda ev: 1 / 0)
    linearjeph.capture_message("x")
    assert linearjeph.flush()
    assert _H.got == []


def test_sampling(server):
    _init(server, sample_rate=0.0, traces_sample_rate=1.0)
    linearjeph.capture_message("dropped")
    linearjeph.capture_transaction("kept", 1)
    assert linearjeph.flush()
    assert [e["type"] for e in _events()] == ["transaction"]
    _init(server, sample_rate=1.0, traces_sample_rate=0.0)
    linearjeph.capture_message("kept")
    linearjeph.capture_transaction("dropped", 1)
    assert linearjeph.flush()
    assert [e["type"] for e in _events()] == ["transaction", "error"]


def test_set_user_and_tags_limits(server):
    _init(server)
    linearjeph.set_user("u-42")
    for i in range(30):
        linearjeph.set_tag("k%d" % i, "v" * 300)
    linearjeph.capture_message("m")
    assert linearjeph.flush()
    ev = _events()[0]
    assert ev["user"] == {"id": "u-42"} and len(ev["tags"]) == 20 and all(len(v) <= 200 for v in ev["tags"].values())
    linearjeph.set_user(None)
    for i in range(30):
        client._state["tags"].pop("k%d" % i, None)


def test_never_raises_without_init_or_with_bad_input():
    linearjeph.close(1)
    linearjeph.capture_exception(ValueError("x"))
    linearjeph.capture_message(object())
    linearjeph.capture_transaction("n", "not-a-number")
    linearjeph.heartbeat("ok")
    assert linearjeph.report_packages() == 0
    assert linearjeph.init("") is False
    assert linearjeph.init(None) is False
    assert linearjeph.flush(0.1) is True


def test_capture_exception_accepts_instance_and_tuple(server):
    _init(server)
    linearjeph.capture_exception(KeyError("inst"))
    try:
        raise OSError("tup")
    except OSError:
        linearjeph.capture_exception(sys.exc_info())
    assert linearjeph.flush()
    assert {e["exception"]["type"] for e in _events()} == {"KeyError", "OSError"}


def test_event_respects_server_limits(server):
    _init(server)
    linearjeph.capture_message("y" * 5000)
    assert linearjeph.flush()
    assert len(_events()[0]["exception"]["value"]) == 500


def test_excepthook_installed_and_threading(server):
    prev, prev_t = sys.excepthook, threading.excepthook
    try:
        def boom():
            raise ValueError("in thread")
        threading.excepthook = lambda a: None
        client._hooked = False
        linearjeph.init("lj_app_testkey123", endpoint=server, hooks=True, backoff=0.01)
        t = threading.Thread(target=boom)
        t.start()
        t.join()
        assert linearjeph.flush()
        assert any(e["exception"]["value"] == "in thread" for e in _events() if e["type"] == "error")
    finally:
        sys.excepthook, threading.excepthook = prev, prev_t
        client._hooked = False


def test_asgi_middleware_times_and_captures(server):
    from linearjeph.integrations.asgi import LinearjephASGIMiddleware
    _init(server)

    async def app(scope, receive, send):
        if scope.get("path") == "/boom":
            raise RuntimeError("bad")
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": b""})

    mw = LinearjephASGIMiddleware(app, ignore_paths=("/health",))

    async def noop(*a):
        pass

    async def go():
        await mw({"type": "http", "method": "GET", "path": "/ok"}, noop, noop)
        await mw({"type": "http", "method": "GET", "path": "/health"}, noop, noop)
        await mw({"type": "lifespan"}, noop, noop)
        with pytest.raises(RuntimeError):
            await mw({"type": "http", "method": "GET", "path": "/boom"}, noop, noop)
    asyncio.run(go())
    assert linearjeph.flush()
    evs = _events()
    names = [e["transaction"]["name"] for e in evs if e["type"] == "transaction"]
    assert "GET /ok" in names and "GET /health" not in names
    assert any(e["type"] == "error" and e["exception"]["type"] == "RuntimeError" for e in evs)


def test_django_middleware(server):
    pytest.importorskip("django")
    from django.conf import settings
    if not settings.configured:
        settings.configure()
    from django.http import HttpResponse
    from django.test import RequestFactory
    from linearjeph.integrations.django import LinearjephMiddleware
    _init(server)
    mw = LinearjephMiddleware(lambda r: HttpResponse("x"))
    mw(RequestFactory().get("/p"))
    mw.process_exception(RequestFactory().get("/p"), ValueError("view blew up"))
    assert linearjeph.flush()
    evs = _events()
    assert any(e["type"] == "transaction" and e["transaction"]["name"] == "GET /p" for e in evs)
    assert any(e["type"] == "error" and e["exception"]["value"] == "view blew up" for e in evs)


def test_flask_integration_with_stub(server, monkeypatch):
    """Flask is not installed in CI images; exercise init_app against a minimal stand-in for the parts it touches."""
    class Signal:
        def __init__(self):
            self.receivers = []

        def connect(self, fn, sender=None):
            self.receivers.append(fn)

    class App:
        def __init__(self):
            self.before, self.after = [], []

        def before_request(self, f):
            self.before.append(f)
            return f

        def after_request(self, f):
            self.after.append(f)
            return f

    g = types.SimpleNamespace()
    request = types.SimpleNamespace(method="GET", path="/items/5", url_rule=types.SimpleNamespace(rule="/items/<int:id>"))
    sig = Signal()
    mod = types.ModuleType("flask")
    mod.g, mod.request, mod.got_request_exception = g, request, sig
    monkeypatch.setitem(sys.modules, "flask", mod)
    from linearjeph.integrations.flask import init_app
    _init(server)
    app = App()
    init_app(app)
    app.before[0]()
    app.after[0](types.SimpleNamespace(status_code=200))
    sig.receivers[0](app, exception=RuntimeError("flask boom"))
    assert linearjeph.flush()
    evs = _events()
    assert any(e["type"] == "transaction" and e["transaction"]["name"] == "GET /items/<int:id>" for e in evs)
    assert any(e["type"] == "error" and e["exception"]["value"] == "flask boom" for e in evs)


def test_fork_gets_a_fresh_queue_and_thread(server):
    _init(server)
    s = client._state["sender"]
    s.put("/ingest/v1/events", {"type": "x"})
    s._pid = -1  # simulate being a forked child
    s.put("/ingest/v1/events", {"type": "y"})
    assert s._pid != -1 and s._thread.is_alive()
