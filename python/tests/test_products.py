import io
import json
import os
import stat
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

import linearjeph
from linearjeph import ApiError, Client, cli

KEY = "ljk_" + "test1234_" + "x" * 32


class Fake(object):
    """Scripted local API. routes: {(METHOD, path): [response, ...]}; the last response repeats."""

    def __init__(self):
        self.routes = {}
        self.calls = []
        self.srv = None

    def on(self, method, path, *responses):
        self.routes[(method, path)] = list(responses)

    def start(self):
        fake = self

        class H(BaseHTTPRequestHandler):
            def _do(self):
                n = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(n)) if n else None
                path = self.path
                fake.calls.append({"method": self.command, "path": path, "body": body, "auth": self.headers.get("Authorization"),
                                   "ua": self.headers.get("User-Agent")})
                seq = fake.routes.get((self.command, path.split("?")[0]))
                if not seq:
                    status, payload, headers = 404, {"error": {"code": "not_found", "message": "Not found."}}, {}
                else:
                    item = seq.pop(0) if len(seq) > 1 else seq[0]
                    status, payload, headers = item if len(item) == 3 else (item[0], item[1], {})
                raw = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/zip" if isinstance(payload, bytes) else "application/json")
                self.send_header("Content-Length", str(len(raw)))
                for k, v in headers.items():
                    self.send_header(k, v)
                self.end_headers()
                self.wfile.write(raw)

            do_GET = do_POST = do_PATCH = do_DELETE = _do

            def log_message(self, *a):
                pass

        self.srv = HTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        return "http://127.0.0.1:%d" % self.srv.server_address[1]

    def stop(self):
        self.srv.shutdown()
        self.srv.server_close()


@pytest.fixture
def fake():
    f = Fake()
    f.url = f.start()
    yield f
    f.stop()


@pytest.fixture
def lj(fake):
    c = Client(api_key=KEY, base_url=fake.url)
    c._sleep = lambda s: None
    return c


@pytest.fixture(autouse=True)
def _env(tmp_path, monkeypatch):
    monkeypatch.setattr("time.sleep", lambda s: None)
    monkeypatch.delenv("LJ_API_KEY", raising=False)
    monkeypatch.delenv("LJ_BASE_URL", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "cfg"))


def run_cli(fake, *argv, **kw):
    out, err = io.StringIO(), io.StringIO()
    real = cli.Out

    class O(real):
        def __init__(self, j):
            real.__init__(self, j, out, err)
    cli.Out = O
    try:
        code = cli.main(["--key", KEY, "--base-url", fake.url] + list(argv) if kw.get("auth", True) else list(argv))
    finally:
        cli.Out = real
    return code, out.getvalue(), err.getvalue()


# ----------------------------------------------------------------------------------------------- client
def test_exports_and_defaults(monkeypatch):
    assert linearjeph.Client is Client and linearjeph.ApiError is ApiError
    monkeypatch.setenv("LJ_API_KEY", KEY)
    monkeypatch.setenv("LJ_BASE_URL", "http://example.invalid/")
    c = Client()
    assert c.api_key == KEY and c.base_url == "http://example.invalid"
    assert Client(api_key="a").base_url == "http://example.invalid"
    monkeypatch.delenv("LJ_BASE_URL")
    assert Client().base_url == "https://linearjeph.cc"


def test_request_headers_body_and_whoami(fake, lj):
    fake.on("GET", "/api/v1/apikeys/whoami", (200, {"organization": {"id": 1, "name": "Acme"}, "scopes": ["fix"], "key": {}}))
    assert lj.whoami()["organization"]["name"] == "Acme"
    fake.on("POST", "/api/v1/fixops/jobs", (201, {"id": 5, "status": "queued"}))
    job = lj.fix.create_job("gh:a/b", "it broke", title="t")
    assert job["id"] == 5
    assert fake.calls[0]["auth"] == "Bearer " + KEY and fake.calls[0]["ua"].startswith("linearjeph-python/")
    assert fake.calls[1]["body"]["source_id"] == "gh:a/b" and fake.calls[1]["body"]["issue"] == "it broke"


def test_error_shape_and_org_query(fake, lj):
    fake.on("GET", "/api/v1/fixops/overview", (402, {"error": {"code": "not_entitled", "message": "Subscribe first."}}))
    lj.org = 7
    with pytest.raises(ApiError) as ei:
        lj.fix.overview()
    assert (ei.value.status, ei.value.code, ei.value.message) == (402, "not_entitled", "Subscribe first.")
    assert fake.calls[0]["path"] == "/api/v1/fixops/overview?org=7"


def test_get_retries_429_and_5xx_post_does_not(fake, lj):
    slept = []
    lj._sleep = slept.append
    fake.on("GET", "/api/v1/fixops/jobs", (429, {"error": {"code": "throttled", "message": "slow"}}, {"Retry-After": "2"}),
            (503, {"error": {"code": "x", "message": "x"}}), (200, {"results": []}))
    assert lj.fix.jobs() == {"results": []}
    assert slept[0] == 2.0 and len(slept) == 2 and len(fake.calls) == 3
    fake.on("POST", "/api/v1/fixops/jobs/1/cancel", (503, {"error": {"code": "x", "message": "down"}}), (200, {}))
    with pytest.raises(ApiError):
        lj.fix.cancel(1)
    assert sum(1 for c in fake.calls if c["method"] == "POST") == 1


def test_get_gives_up_after_three_tries(fake, lj):
    fake.on("GET", "/api/v1/shield/overview", (500, {"error": {"code": "boom", "message": "bad"}}))
    with pytest.raises(ApiError) as ei:
        lj.shield.overview()
    assert ei.value.status == 500 and len(fake.calls) == 3


def test_network_error():
    c = Client(api_key=KEY, base_url="http://127.0.0.1:1", timeout=1)
    c._sleep = lambda s: None
    with pytest.raises(ApiError) as ei:
        c.whoami()
    assert ei.value.status == 0 and ei.value.code == "network_error"


def test_generic_request_paths_and_text(fake, lj):
    fake.on("GET", "/api/v1/comply/overview", (200, {"ok": 1}))
    assert lj.request("GET", "/api/v1/comply/overview") == {"ok": 1}
    assert lj.request("GET", "/comply/overview") == {"ok": 1}
    assert lj.request("GET", "comply/overview", query={"a": True})
    assert fake.calls[-1]["path"].endswith("?a=true")


# ----------------------------------------------------------------------------------------------- namespaces
def test_namespace_routes_methods_and_bodies(fake, lj):
    table = [
        (lambda: lj.fix.reject(3, "no"), "POST", "/api/v1/fixops/jobs/3/reject", {"reason": "no"}),
        (lambda: lj.fix.review(2), "POST", "/api/v1/fixops/projects/2/review-now", {}),
        (lambda: lj.fix.deps(2), "POST", "/api/v1/fixops/projects/2/deps-now", {}),
        (lambda: lj.fix.tests(2, 3), "POST", "/api/v1/fixops/projects/2/tests-now", {"max_files": 3}),
        (lambda: lj.fix.plan(4), "POST", "/api/v1/fixops/migrations/4/plan", {}),
        (lambda: lj.fix.events(9, after=4), "GET", "/api/v1/fixops/jobs/9/events?after=4", None),
        (lambda: lj.shield.vibe_scan(12), "POST", "/api/v1/shield/vibe/projects/12/scan", {}),
        (lambda: lj.shield.vibe_scan_detail(5), "GET", "/api/v1/shield/vibe/scans/5", None),
        (lambda: lj.shield.secrets_scan(8), "POST", "/api/v1/shield/secrets/sources/8/scan", {}),
        (lambda: lj.shield.secrets_findings("open"), "GET", "/api/v1/shield/secrets/findings?status=open", None),
        (lambda: lj.shield.pentest_scan(6), "POST", "/api/v1/shield/pentest/targets/6/scan", {}),
        (lambda: lj.shield.pentest_retest(11), "POST", "/api/v1/shield/pentest/findings/11/retest", {}),
        (lambda: lj.shield.security_scan(2), "POST", "/api/v1/security/scans", {"target": 2}),
        (lambda: lj.shield.waf_events(3), "GET", "/api/v1/shield/waf/domains/3/events", None),
        (lambda: lj.ops.resolve(3), "POST", "/api/v1/opsagent/incidents/3/resolve", {}),
        (lambda: lj.ops.cost_analyse(), "POST", "/api/v1/opsagent/cost/analyse", {}),
        (lambda: lj.ops.backup_run(4), "POST", "/api/v1/backups/agent/targets/4/run", {}),
        (lambda: lj.ops.backup_drill(4), "POST", "/api/v1/backups/agent/targets/4/drill", {}),
        (lambda: lj.ops.backup_runs(4), "GET", "/api/v1/backups/agent/targets/4/runs", None),
        (lambda: lj.ops.incidents("open"), "GET", "/api/v1/opsagent/incidents?status=open", None),
        (lambda: lj.models.endpoints(), "GET", "/api/v1/endpoints/endpoints", None),
        (lambda: lj.models.action(2, "rotate-key"), "POST", "/api/v1/endpoints/endpoints/2/rotate-key", {}),
        (lambda: lj.models.private_logs(2), "GET", "/api/v1/agentcloud/private/endpoints/2/logs", None),
        (lambda: lj.models.finetune_jobs(), "GET", "/api/v1/agentcloud/finetune/jobs", None),
        (lambda: lj.agents.run(7, {"q": 1}), "POST", "/api/v1/agentcloud/agents/7/run", {"input": {"q": 1}}),
        (lambda: lj.agents.pause(7), "POST", "/api/v1/agentcloud/agents/7/pause", {}),
        (lambda: lj.agents.run_detail(7, 3), "GET", "/api/v1/agentcloud/agents/7/runs/3", None),
        (lambda: lj.comply.recheck(), "POST", "/api/v1/comply/compliance/recheck", {}),
        (lambda: lj.comply.enable_framework("soc2"), "POST", "/api/v1/comply/compliance/frameworks", {"key": "soc2"}),
        (lambda: lj.comply.update_task(3, status="done"), "PATCH", "/api/v1/comply/compliance/tasks/3", {"status": "done"}),
    ]
    for call, method, path, body in table:
        fake.on(method, path.split("?")[0], (200, {"ok": True}))
        call()
        last = fake.calls[-1]
        assert (last["method"], last["path"], last["body"]) == (method, path, body), path
    with pytest.raises(ValueError):
        lj.models.action(1, "../x")


def test_fix_patch_and_comply_export_bytes(fake, lj):
    fake.on("GET", "/api/v1/fixops/jobs/1/patch", (200, b"diff --git a b\n"))
    assert lj.fix.patch(1).startswith("diff --git")
    fake.on("GET", "/api/v1/comply/compliance/export", (200, b"PK\x03\x04zip"))
    assert lj.comply.export() == b"PK\x03\x04zip"


def test_fix_wait_stops_at_final_or_pr_open(fake, lj):
    seen = []
    fake.on("GET", "/api/v1/fixops/jobs/1", (200, {"status": "queued"}), (200, {"status": "patching"}), (200, {"status": "done"}))
    assert lj.fix.wait(1, on_update=lambda j: seen.append(j["status"]))["status"] == "done"
    assert seen == ["queued", "patching", "done"]
    fake.on("GET", "/api/v1/fixops/jobs/2", (200, {"status": "pr_open"}))
    assert lj.fix.wait(2)["status"] == "pr_open"
    fake.on("GET", "/api/v1/fixops/jobs/3", (200, {"status": "testing"}))
    with pytest.raises(TimeoutError):
        lj.fix.wait(3, timeout=0, interval=0)


def test_gate_orders_severities(lj):
    scan = {"findings": [{"severity": s} for s in ("info", "low", "medium", "high", "critical")]}
    assert [f["severity"] for f in lj.shield.gate(scan, "high")] == ["high", "critical"]
    assert len(lj.shield.gate(scan, "low")) == 4
    assert lj.shield.gate({"findings": []}) == []
    with pytest.raises(ValueError):
        lj.shield.gate(scan, "severe")


def test_wait_scan_and_agent_run(fake, lj):
    fake.on("GET", "/api/v1/shield/vibe/scans/5", (200, {"status": "running"}), (200, {"status": "done", "findings": []}))
    assert lj.shield.wait_scan(5)["status"] == "done"
    fake.on("GET", "/api/v1/agentcloud/agents/1/runs/2", (200, {"status": "running"}), (200, {"status": "succeeded"}))
    assert lj.agents.wait_run(1, 2)["status"] == "succeeded"


# ----------------------------------------------------------------------------------------------- cli
def _scan_routes(fake, findings, status="done"):
    fake.on("POST", "/api/v1/shield/vibe/projects/12/scan", (201, {"id": 5, "status": "queued"}))
    fake.on("GET", "/api/v1/shield/vibe/scans/5", (200, {"id": 5, "status": status, "counts": {"high": 1}, "findings": findings}))


def test_cli_shield_gate_exit_codes(fake):
    _scan_routes(fake, [{"severity": "low", "title": "Meh"}])
    code, out, _ = run_cli(fake, "shield", "gate", "--project", "12", "--fail-on", "high")
    assert code == 0 and "gate passed" in out
    _scan_routes(fake, [{"severity": "high", "title": "Open bucket", "category": "storage"}])
    code, out, _ = run_cli(fake, "shield", "gate", "--project", "12", "--fail-on", "high")
    assert code == 1 and "FAILED" in out and "Open bucket" in out
    code, out, _ = run_cli(fake, "--json", "shield", "gate", "--project", "12", "--fail-on", "critical")
    assert code == 0 and json.loads(out)["passed"] is True
    _scan_routes(fake, [], status="failed")
    code, _, _ = run_cli(fake, "shield", "gate", "--project", "12")
    assert code == 1


def test_cli_fix_run_wait(fake):
    fake.on("GET", "/api/v1/fixops/projects/3", (200, {"id": 3, "source_id": "gh:acme/app"}))
    fake.on("POST", "/api/v1/fixops/jobs", (201, {"id": 9, "status": "queued"}))
    fake.on("GET", "/api/v1/fixops/jobs/9", (200, {"id": 9, "status": "patching"}), (200, {"id": 9, "status": "done", "title": "Fixed", "events": []}))
    code, out, err = run_cli(fake, "fix", "run", "--project", "3", "--issue", "bug", "--wait", "--json")
    assert code == 0 and json.loads(out)["status"] == "done"
    assert [c for c in fake.calls if c["method"] == "POST"][0]["body"]["source_id"] == "gh:acme/app"
    code, out, err = run_cli(fake, "fix", "run", "--project", "3", "--issue", "bug", "--wait")
    assert code == 0 and "job 9" in err
    fake.on("GET", "/api/v1/fixops/jobs/9", (200, {"id": 9, "status": "failed", "error": "tests red"}))
    code, out, _ = run_cli(fake, "fix", "wait", "9")
    assert code == 1 and "tests red" in out


def test_cli_fix_accept_status_tables(fake):
    fake.on("GET", "/api/v1/fixops/jobs", (200, {"results": [{"id": 1, "status": "done", "kind": "fix", "title": "T", "pr_url": "http://x/1"}]}))
    code, out, _ = run_cli(fake, "fix", "jobs")
    assert code == 0 and out.splitlines()[0].split()[:3] == ["ID", "STATUS", "KIND"] and "http://x/1" in out
    fake.on("POST", "/api/v1/fixops/jobs/1/accept", (200, {"id": 1, "status": "accepted"}))
    assert run_cli(fake, "fix", "accept", "1")[0] == 0


def test_cli_auth_and_usage_errors(fake, monkeypatch):
    code, _, err = run_cli(fake, "whoami", auth=False)
    assert code == 2 and "no API key" in err
    fake.on("GET", "/api/v1/apikeys/whoami", (401, {"error": {"code": "unauthorized", "message": "Bad key."}}))
    code, _, err = run_cli(fake, "whoami")
    assert code == 2 and "Bad key." in err and KEY not in err
    with pytest.raises(SystemExit) as ei:
        cli.main(["fix", "run", "--project", "1"])
    assert ei.value.code == 2
    with pytest.raises(SystemExit) as ei:
        cli.main(["shield", "gate", "--project", "1", "--fail-on", "nope"])
    assert ei.value.code == 2
    code, _, err = run_cli(fake, "fix", "status", "abc")
    assert code == 2


def test_cli_login_logout_whoami_file_mode(fake, tmp_path, capsys, monkeypatch):
    fake.on("GET", "/api/v1/apikeys/whoami", (200, {"organization": {"id": 1, "name": "Acme"}, "scopes": ["fix", "shield"],
                                                    "key": {"prefix": "test1234", "name": "ci", "expires_at": None}}))
    monkeypatch.setenv("LJ_BASE_URL", fake.url)
    code = cli.main(["login", "--key", KEY])
    captured = capsys.readouterr()
    assert code == 0 and KEY not in captured.out + captured.err and "Acme" in captured.out
    path = cli.credentials_path()
    assert path == str(tmp_path / "cfg" / "linearjeph" / "credentials")
    assert stat.S_IMODE(os.stat(path).st_mode) == 0o600
    assert json.load(open(path))["api_key"] == KEY
    # the saved key is used, never echoed
    assert cli.main(["whoami"]) == 0
    out = capsys.readouterr().out
    assert "Acme" in out and "fix, shield" in out and KEY not in out
    assert fake.calls[-1]["auth"] == "Bearer " + KEY
    assert cli.main(["logout"]) == 0
    assert not os.path.exists(path)
    assert cli.main(["whoami"]) == 2


def test_cli_login_rejects_bad_key_and_keeps_nothing(fake, capsys):
    fake.on("GET", "/api/v1/apikeys/whoami", (401, {"error": {"code": "unauthorized", "message": "Bad key."}}))
    assert cli.main(["login", "--key", KEY, "--base-url", fake.url]) == 2
    assert not os.path.exists(cli.credentials_path())


def test_cli_key_precedence(fake, monkeypatch):
    other = "ljk_" + "other123_" + "y" * 32
    cli.save_credentials(other)
    monkeypatch.setenv("LJ_API_KEY", "ljk_" + "envkey12_" + "z" * 32)
    fake.on("GET", "/api/v1/apikeys/whoami", (200, {"organization": {}, "scopes": [], "key": {}}))
    cli.main(["--base-url", fake.url, "whoami"])
    assert fake.calls[-1]["auth"].endswith("z" * 32)
    cli.main(["--base-url", fake.url, "--key", KEY, "whoami"])
    assert fake.calls[-1]["auth"] == "Bearer " + KEY
    monkeypatch.delenv("LJ_API_KEY")
    cli.main(["--base-url", fake.url, "whoami"])
    assert fake.calls[-1]["auth"] == "Bearer " + other


def test_cli_other_products(fake, tmp_path):
    fake.on("GET", "/api/v1/opsagent/incidents", (200, {"results": [{"id": 1, "status": "open", "severity": "critical", "title": "Disk"}]}))
    code, out, _ = run_cli(fake, "ops", "incidents", "--status", "open")
    assert code == 0 and "Disk" in out and fake.calls[-1]["path"].endswith("status=open")
    fake.on("POST", "/api/v1/opsagent/incidents/1/resolve", (200, {"id": 1, "status": "resolved"}))
    assert run_cli(fake, "ops", "resolve", "1")[0] == 0
    fake.on("POST", "/api/v1/backups/agent/targets/4/run", (202, {"id": 1, "status": "queued"}))
    assert run_cli(fake, "ops", "backup", "4")[0] == 0
    fake.on("GET", "/api/v1/endpoints/endpoints", (200, {"results": [{"id": 2, "name": "chat", "status": "active"}]}))
    fake.on("GET", "/api/v1/agentcloud/private/endpoints", (402, {"error": {"code": "not_entitled", "message": "n"}}))
    code, out, _ = run_cli(fake, "models", "list")
    assert code == 0 and "chat" in out
    fake.on("POST", "/api/v1/agentcloud/agents/7/run", (202, {"id": 3, "status": "queued"}))
    fake.on("GET", "/api/v1/agentcloud/agents/7/runs/3", (200, {"id": 3, "status": "failed", "error": "boom"}))
    assert run_cli(fake, "agents", "run", "7", "--input", '{"a": 1}', "--wait")[0] == 1
    assert [c for c in fake.calls if c["method"] == "POST"][-1]["body"] == {"input": {"a": 1}}
    fake.on("GET", "/api/v1/comply/compliance/export", (200, b"PK\x03\x04data"))
    dest = tmp_path / "a.zip"
    code, out, _ = run_cli(fake, "comply", "export", "--out", str(dest))
    assert code == 0 and dest.read_bytes() == b"PK\x03\x04data"
    fake.on("GET", "/api/v1/comply/compliance", (200, {"frameworks": [{"key": "soc2", "name": "SOC 2", "score": 80}], "checks": []}))
    assert "SOC 2" in run_cli(fake, "comply", "status")[1]


def test_cli_shield_findings_and_api(fake):
    fake.on("GET", "/api/v1/shield/secrets/findings", (200, {"results": [{"severity": "critical", "rule": "aws", "file": "a.py"},
                                                                          {"severity": "low", "rule": "x", "file": "b.py"}]}))
    code, out, _ = run_cli(fake, "shield", "findings", "--severity", "high")
    assert code == 0 and "aws" in out and "b.py" not in out
    fake.on("POST", "/api/v1/fixops/jobs/1/cancel", (200, {"id": 1, "status": "cancelled"}))
    code, out, _ = run_cli(fake, "api", "POST", "fixops/jobs/1/cancel", "--data", "{}")
    assert code == 0 and json.loads(out)["status"] == "cancelled"
    code, _, err = run_cli(fake, "api", "GET", "fixops/missing")
    assert code == 2 and "not_found" in err


def test_cli_help_lists_everything(capsys):
    for argv, words in ((["--help"], ["fix", "shield", "ops", "models", "agents", "comply", "api", "login"]),
                        (["shield", "--help"], ["scan", "findings", "gate", "secrets", "pentest", "waf"]),
                        (["fix", "--help"], ["jobs", "run", "status", "wait", "accept", "reject", "rollback", "review", "deps", "tests"])):
        with pytest.raises(SystemExit) as ei:
            cli.main(argv)
        assert ei.value.code == 0
        text = capsys.readouterr().out
        assert all(w in text for w in words)
    with pytest.raises(SystemExit) as ei:
        cli.main(["shield"])
    assert ei.value.code == 0


def test_python38_syntax():
    import ast
    here = os.path.dirname(cli.__file__)
    for name in ("cli.py", "products.py"):
        with open(os.path.join(here, name)) as fh:
            ast.parse(fh.read(), feature_version=(3, 8))
