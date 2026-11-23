"""The `lj` command line for the LINEARJEPH product SDKs. Standard library only.

    lj login | logout | whoami
    lj fix | shield | ops | models | agents | comply ...
    lj api GET fixops/jobs

Key lookup: --key, then $LJ_API_KEY, then ~/.config/linearjeph/credentials. Output is a table, or raw JSON with --json.
Exit codes: 0 ok, 1 gate or job failed, 2 usage, auth or API error.
"""
import argparse
import getpass
import json
import os
import sys

from . import products
from .products import ApiError, Client, severity_rank

EXIT_OK, EXIT_FAIL, EXIT_USAGE = 0, 1, 2
PRODUCTS = ("fix", "shield", "ops", "models", "agents", "comply")


class UsageError(Exception):
    pass


# ------------------------------------------------------------------------------------------------ credentials
def credentials_path():
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config")
    return os.path.join(base, "linearjeph", "credentials")


def load_credentials():
    """Return {"api_key": ..., "base_url": ...} from the credentials file ({} if absent or unreadable)."""
    try:
        with open(credentials_path(), "r") as fh:
            text = fh.read().strip()
    except OSError:
        return {}
    if not text:
        return {}
    try:
        data = json.loads(text)
        if isinstance(data, dict):
            return {"api_key": str(data.get("api_key") or ""), "base_url": str(data.get("base_url") or "")}
    except ValueError:
        pass
    return {"api_key": text.splitlines()[0].strip(), "base_url": ""}


def save_credentials(api_key, base_url=None):
    path = credentials_path()
    folder = os.path.dirname(path)
    os.makedirs(folder, mode=0o700, exist_ok=True)
    payload = {"api_key": api_key}
    if base_url:
        payload["base_url"] = base_url
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        fh.write(json.dumps(payload) + "\n")
    try:
        os.chmod(path, 0o600)
    except OSError:
        pass
    return path


def delete_credentials():
    try:
        os.remove(credentials_path())
        return True
    except FileNotFoundError:
        return False


# ------------------------------------------------------------------------------------------------ output
def _cell(value):
    if value is None:
        return "-"
    if isinstance(value, bool):
        return "yes" if value else "no"
    if isinstance(value, (dict, list)):
        return json.dumps(value, separators=(",", ":"), sort_keys=True)
    return " ".join(str(value).split())


def _get(row, path):
    cur = row
    for part in path.split("."):
        if isinstance(cur, dict):
            cur = cur.get(part)
        else:
            return None
    return cur


def render_table(rows, columns):
    """columns: list of (HEADER, key path or callable). Returns text."""
    if not rows:
        return "(none)"
    head = [c[0] for c in columns]
    body = []
    for r in rows:
        line = []
        for _, src in columns:
            v = src(r) if callable(src) else _get(r, src)
            s = _cell(v)
            line.append(s if len(s) <= 60 else s[:57] + "...")
        body.append(line)
    widths = [max(len(h), *(len(b[i]) for b in body)) for i, h in enumerate(head)]
    out = ["  ".join(h.ljust(widths[i]) for i, h in enumerate(head)).rstrip()]
    for b in body:
        out.append("  ".join(c.ljust(widths[i]) for i, c in enumerate(b)).rstrip())
    return "\n".join(out)


def render_kv(data, keys=None):
    keys = keys or [k for k, v in data.items() if not isinstance(v, (dict, list))]
    keys = [k for k in keys if k in data]
    if not keys:
        return "(empty)"
    w = max(len(k) for k in keys)
    return "\n".join("%s  %s" % (k.ljust(w), _cell(data[k])) for k in keys)


def _results(data):
    if isinstance(data, dict):
        r = data.get("results")
        if isinstance(r, list):
            return r
    return data if isinstance(data, list) else []


class Out(object):
    def __init__(self, as_json, stdout=None, stderr=None):
        self.as_json = as_json
        self.stdout = stdout or sys.stdout
        self.stderr = stderr or sys.stderr

    def emit(self, data, text_fn):
        if self.as_json:
            self.stdout.write(json.dumps(data, indent=2, sort_keys=True, default=str) + "\n")
        else:
            self.stdout.write(text_fn(data) + "\n")

    def note(self, msg):
        self.stderr.write(msg + "\n")

    def progress(self, label):
        def cb(obj):
            if not self.as_json:
                self.note("%s: %s" % (label, obj.get("status")))
        return cb


# ------------------------------------------------------------------------------------------------ commands
# Each handler: fn(client, args, out) -> exit code.

def _id(value, what="id"):
    try:
        return int(value)
    except (TypeError, ValueError):
        raise UsageError("%s must be a number, got %r" % (what, value))


def _json_arg(text, what):
    if text is None:
        return None
    if text.startswith("@"):
        try:
            with open(text[1:], "r") as fh:
                text = fh.read()
        except OSError as exc:
            raise UsageError("cannot read %s: %s" % (text[1:], exc))
    try:
        return json.loads(text)
    except ValueError as exc:
        raise UsageError("%s is not valid JSON: %s" % (what, exc))


JOB_COLS = [("ID", "id"), ("STATUS", "status"), ("KIND", "kind"), ("TITLE", "title"), ("PR", "pr_url"), ("CREATED", "created_at")]


def _job_text(j):
    text = render_kv(j, ["id", "kind", "title", "status", "stage_note", "project", "risk", "pr_url", "branch", "iterations",
                         "tests_before", "tests_after", "root_cause", "summary", "error", "created_at", "finished_at"])
    ev = j.get("events") or []
    if ev:
        text += "\n\nEvents\n" + render_table(ev[-15:], [("AT", "at"), ("STAGE", "stage"), ("MESSAGE", "message")])
    return text


def _job_exit(job):
    return EXIT_FAIL if job.get("status") in products.FAILED_JOB else EXIT_OK


def _wait_job(c, job_id, args, out):
    job = c.fix.wait(job_id, timeout=args.timeout, on_update=out.progress("job %s" % job_id))
    full = c.fix.job(job_id) if job.get("events") is None else job
    out.emit(full, _job_text)
    return _job_exit(full)


def cmd_fix(c, a, out):
    sub = a.fix_cmd
    if sub == "jobs":
        out.emit(c.fix.jobs(status=a.status), lambda d: render_table(_results(d), JOB_COLS))
    elif sub == "run":
        source = a.source
        if a.project is not None:
            if str(a.project).isdigit():
                source = c.fix.project(int(a.project)).get("source_id")
            else:
                source = a.project
        if not source:
            raise UsageError("give --project <id> (or --source <source id>)")
        job = c.fix.create_job(source, a.issue, title=a.title or "", logs=a.logs or "", repro_command=a.repro_command or "",
                               test_command=a.test_command or "", url=a.url or "")
        if not a.wait:
            out.emit(job, _job_text)
            return EXIT_OK
        out.note("job %s: %s" % (job["id"], job.get("status")))
        return _wait_job(c, job["id"], a, out)
    elif sub == "status":
        out.emit(c.fix.job(_id(a.job)), _job_text)
    elif sub == "wait":
        return _wait_job(c, _id(a.job), a, out)
    elif sub == "accept":
        out.emit(c.fix.accept(_id(a.job)), _job_text)
    elif sub == "reject":
        res = c.fix.reject(_id(a.job), a.reason or "")
        out.emit(res, lambda d: _job_text(d.get("job") or d) + ("\nrefunded  %s" % d["refunded"] if d.get("refunded") else ""))
    elif sub == "rollback":
        out.emit(c.fix.rollback(_id(a.job)), lambda d: render_kv(d) if isinstance(d, dict) else str(d))
    elif sub in ("review", "deps", "tests"):
        pid = _id(a.project, "project")
        res = getattr(c.fix, sub)(pid)
        out.emit(res, lambda d: render_kv(d) if isinstance(d, dict) else str(d))
    return EXIT_OK


def _scan_text(s):
    text = render_kv(s, ["id", "status", "host", "trigger", "score", "grade", "counts", "error", "finished_at"])
    f = s.get("findings")
    if f:
        text += "\n\n" + _findings_table(f)
    return text


def _findings_table(rows):
    rows = sorted(rows, key=lambda f: -severity_rank(f.get("severity")))
    return render_table(rows, [("SEVERITY", "severity"), ("TITLE", lambda f: f.get("title") or f.get("rule") or f.get("key")),
                               ("WHERE", lambda f: f.get("location") or f.get("file") or f.get("category")),
                               ("STATUS", "status")])


def _vibe_scan(c, a, out):
    pid = _id(a.project, "project")
    scan = c.shield.vibe_scan(pid)
    if not a.wait:
        return scan, False
    out.note("scan %s: %s" % (scan["id"], scan.get("status")))
    return c.shield.wait_scan(scan["id"], "vibe", timeout=a.timeout, on_update=out.progress("scan %s" % scan["id"])), True


def cmd_shield(c, a, out):
    sub = a.shield_cmd
    if sub == "scan":
        scan, waited = _vibe_scan(c, a, out)
        out.emit(scan, _scan_text)
        return EXIT_FAIL if scan.get("status") == "failed" else EXIT_OK
    if sub == "gate":
        a.wait = True
        scan, _ = _vibe_scan(c, a, out)
        hits = c.shield.gate(scan, a.fail_on) if scan.get("status") == "done" else []
        passed = scan.get("status") == "done" and not hits
        payload = {"passed": passed, "fail_on": a.fail_on,
                   "scan": {"id": scan.get("id"), "status": scan.get("status"), "counts": scan.get("counts"),
                            "error": scan.get("error")}, "findings": hits}

        def text(d):
            head = "gate %s: scan %s %s, %d finding(s) at or above %s" % (
                "passed" if d["passed"] else "FAILED", d["scan"]["id"], d["scan"]["status"], len(d["findings"]), d["fail_on"])
            if d["scan"].get("error"):
                head += "\nerror  " + _cell(d["scan"]["error"])
            return head + ("\n\n" + _findings_table(d["findings"]) if d["findings"] else "")
        out.emit(payload, text)
        return EXIT_OK if passed else EXIT_FAIL
    if sub == "findings":
        if a.project is not None:
            pid = _id(a.project, "project")
            proj = next((p for p in c.shield.vibe_projects().get("projects", []) if p.get("id") == pid), None)
            last = (proj or {}).get("last_scan") or {}
            if not last.get("id"):
                raise UsageError("project %s has no scan yet; run `lj shield scan --project %s`" % (pid, pid))
            rows = c.shield.vibe_scan_detail(last["id"]).get("findings") or []
        else:
            rows = _results(c.shield.secrets_findings(a.status))
        if a.severity:
            floor = severity_rank(a.severity)
            rows = [f for f in rows if severity_rank(f.get("severity")) >= floor]
        out.emit({"results": rows}, lambda d: _findings_table(d["results"]) if d["results"] else "(none)")
    elif sub == "secrets":
        def text(d):
            t = "Sources\n" + render_table(d.get("sources") or [], [("ID", "id"), ("KIND", "kind"), ("REF", "ref"), ("OPEN", "open"),
                                                                    ("LAST SCAN", "last_scan.status")])
            return t + "\n\nFindings by status\n" + render_kv(d.get("counts") or {})
        out.emit(c.shield.secrets_overview(), text)
    elif sub == "pentest":
        res = c.shield.pentest_scan(_id(a.target, "target"))
        if a.wait:
            out.note("scan %s: %s" % (res["id"], res.get("status")))
            res = c.shield.wait_scan(res["id"], "pentest", timeout=a.timeout, on_update=out.progress("scan %s" % res["id"]))
        out.emit(res, _scan_text)
        return EXIT_FAIL if res.get("status") == "failed" else EXIT_OK
    elif sub == "waf":
        def text(d):
            return "Protected domains\n" + render_table(d.get("domains") or [], [("ID", "id"), ("DOMAIN", "domain"), ("STATE", "state"),
                                                                                  ("ENABLED", "enabled"), ("SYNCED", "synced_at")])
        out.emit(c.shield.waf_overview(), text)
    return EXIT_OK


def cmd_ops(c, a, out):
    sub = a.ops_cmd
    if sub == "status":
        def text(d):
            o = d.get("oncall") or {}
            return render_kv(o, ["servers_used", "servers_limit", "incidents_this_period", "incidents_limit", "auto_fix_allowed"])
        out.emit(c.ops.summary(), text)
    elif sub == "incidents":
        out.emit(c.ops.incidents(a.status), lambda d: render_table(_results(d), [
            ("ID", "id"), ("STATUS", "status"), ("SEVERITY", "severity"), ("TYPE", "type"), ("SERVER", "server"),
            ("TITLE", "title")]))
    elif sub == "resolve":
        out.emit(c.ops.resolve(_id(a.id)), lambda d: render_kv(d))
    elif sub == "cost":
        def text(d):
            t = render_kv(d.get("totals") or {})
            return t + "\n\nRecommendations\n" + render_table(d.get("recommendations") or [], [
                ("ID", "id"), ("STATUS", "status"), ("TITLE", "title"), ("SAVING", "monthly_saving")])
        out.emit(c.ops.cost(), text)
    elif sub == "backup":
        out.emit(c.ops.backup_run(_id(a.target, "target")), lambda d: render_kv(d))
    elif sub == "drill":
        out.emit(c.ops.backup_drill(_id(a.target, "target")), lambda d: render_kv(d))
    return EXIT_OK


def cmd_models(c, a, out):
    sub = a.models_cmd
    if sub == "list":
        res = {"endpoints": _results(c.models.endpoints()), "private": []}
        try:
            res["private"] = _results(c.models.private_endpoints())
        except ApiError as exc:
            if exc.status not in (401, 402, 403, 404):
                raise
        cols = [("ID", "id"), ("NAME", "name"), ("MODEL", "model"), ("STATUS", "status")]

        def text(d):
            t = "AI endpoints\n" + render_table(d["endpoints"], cols)
            if d["private"]:
                t += "\n\nPrivate endpoints\n" + render_table(d["private"], cols)
            return t
        out.emit(res, text)
    elif sub == "usage":
        eid = _id(a.id)
        data = c.models.private_usage(eid) if a.private else c.models.usage(eid)
        out.emit(data, lambda d: (render_kv(d["summary"]) + "\n\n" if isinstance(d.get("summary"), dict) else "") +
                 render_table(_results(d)[-30:], [(k.upper().replace("_", " "), k) for k in
                                                  (sorted(_results(d)[0].keys()) if _results(d) else [])]))
    elif sub == "finetune":
        out.emit(c.models.finetune_jobs(), lambda d: render_table(_results(d), [
            ("ID", "id"), ("NAME", "name"), ("STATUS", "status"), ("BASE", "base_model"), ("PROGRESS", "progress"),
            ("DATASET", "dataset")]))
    return EXIT_OK


def cmd_agents(c, a, out):
    sub = a.agents_cmd
    if sub == "list":
        out.emit(c.agents.list(), lambda d: render_table(_results(d), [
            ("ID", "id"), ("NAME", "name"), ("STATUS", "status"), ("SCHEDULE", "schedule"), ("LAST RUN", "last_run.status")]))
    elif sub == "run":
        aid = _id(a.id)
        run = c.agents.run(aid, _json_arg(a.input, "--input"))
        if a.wait:
            out.note("run %s: %s" % (run["id"], run.get("status")))
            run = c.agents.wait_run(aid, run["id"], timeout=a.timeout, on_update=out.progress("run %s" % run["id"]))
        out.emit(run, lambda r: render_kv(r, ["id", "agent_id", "status", "trigger", "seconds", "exit_code", "error", "output"]))
        return EXIT_FAIL if run.get("status") in ("failed", "timeout", "cancelled") else EXIT_OK
    elif sub == "runs":
        out.emit(c.agents.runs(_id(a.id)), lambda d: render_table(_results(d), [
            ("ID", "id"), ("STATUS", "status"), ("TRIGGER", "trigger"), ("SECONDS", "seconds"), ("QUEUED", "queued_at")]))
    return EXIT_OK


def cmd_comply(c, a, out):
    sub = a.comply_cmd
    if sub == "status":
        def text(d):
            t = "Frameworks\n" + render_table(d.get("frameworks") or [], [
                ("KEY", "key"), ("NAME", "name"), ("SCORE", lambda f: f.get("score", f.get("percent"))),
                ("PASSED", "passed"), ("TOTAL", "total")])
            return t + "\n\nChecks\n" + render_table(d.get("checks") or [], [("CHECK", "label"), ("OK", "ok"), ("DETAIL", "detail")])
        out.emit(c.comply.compliance(), text)
    elif sub == "tasks":
        out.emit(c.comply.tasks(), lambda d: render_table(_results(d), [
            ("ID", "id"), ("STATUS", "status"), ("TITLE", "title"), ("DUE", "due_date"), ("CONTROL", "control")]))
    elif sub == "recheck":
        out.emit(c.comply.recheck(), lambda d: render_table(d.get("checks") or [], [("CHECK", "label"), ("OK", "ok"), ("DETAIL", "detail")])
                 if isinstance(d, dict) and d.get("checks") else render_kv(d) if isinstance(d, dict) else str(d))
    elif sub == "export":
        data = c.comply.export()
        path = a.out or "audit-package.zip"
        with open(path, "wb") as fh:
            fh.write(data)
        out.emit({"file": path, "bytes": len(data)}, lambda d: "wrote %s (%d bytes)" % (d["file"], d["bytes"]))
    return EXIT_OK


def cmd_api(c, a, out):
    body = _json_arg(a.data, "--data")
    data = c.request(a.method.upper(), a.path, body)
    if isinstance(data, str):
        out.stdout.write(data if data.endswith("\n") else data + "\n")
    elif data is not None:
        out.stdout.write(json.dumps(data, indent=2, sort_keys=True) + "\n")
    return EXIT_OK


def cmd_whoami(c, a, out):
    def text(d):
        org = d.get("organization") or {}
        key = d.get("key") or {}
        return "\n".join(["organization  %s (id %s)" % (org.get("name"), org.get("id")), "scopes        %s" % ", ".join(d.get("scopes") or []),
                          "key           %s%s" % (key.get("name") or "-", " (%s...)" % key["prefix"] if key.get("prefix") else ""),
                          "expires       %s" % (key.get("expires_at") or "never")])
    out.emit(c.whoami(), text)
    return EXIT_OK


# ------------------------------------------------------------------------------------------------ parser
class _Parser(argparse.ArgumentParser):
    def error(self, message):
        self.print_usage(sys.stderr)
        self.exit(EXIT_USAGE, "%s: error: %s\n" % (self.prog, message))


def _common():
    p = argparse.ArgumentParser(add_help=False, argument_default=argparse.SUPPRESS)
    p.add_argument("--key", help="API key (default: $LJ_API_KEY, then the credentials file)")
    p.add_argument("--base-url", help="API base URL (default: $LJ_BASE_URL or https://linearjeph.cc)")
    p.add_argument("--org", help="organization id, for keys that cover several")
    p.add_argument("--json", action="store_true", help="print raw JSON")
    return p


def build_parser():
    common = _common()
    root = _Parser(prog="lj", parents=[common], formatter_class=argparse.RawDescriptionHelpFormatter,
                   description="LINEARJEPH command line: Fix, Shield, Ops, Models, Agents and Comply.",
                   epilog="Exit codes: 0 ok, 1 gate or job failed, 2 usage, auth or API error.\n"
                          "Run `lj <product> --help` for the commands of a product.")
    root.add_argument("--version", action="version", version="lj " + products.SDK_VERSION)
    top = root.add_subparsers(dest="cmd", metavar="<command>")

    def leaf(sp, name, help_, *args):
        p = sp.add_parser(name, parents=[common], help=help_, description=help_)
        for a in args:
            a(p)
        return p

    def arg(*names, **kw):
        return lambda p: p.add_argument(*names, **kw)

    timeout = arg("--timeout", type=float, default=1800.0, help="seconds to wait (default 1800)")
    wait = arg("--wait", action="store_true", help="wait for the final status")

    def group(name, help_, dest):
        p = top.add_parser(name, parents=[common], help=help_, description=help_)
        return p.add_subparsers(dest=dest, metavar="<command>")

    leaf(top, "login", "Save an API key to the credentials file (mode 0600)", arg("--no-verify", action="store_true", help="skip the whoami check"))
    leaf(top, "logout", "Delete the saved API key")
    leaf(top, "whoami", "Show the organization and scopes of the current key")

    fx = group("fix", "AI Fix: jobs, code review, dependencies, tests", "fix_cmd")
    leaf(fx, "jobs", "List jobs", arg("--status"))
    leaf(fx, "run", "Create a fix job", arg("--project", help="project id (or a source id)"), arg("--source", help="source id"),
         arg("--issue", required=True, help="what is broken"), arg("--title"), arg("--logs"), arg("--repro-command"),
         arg("--test-command"), arg("--url"), wait, timeout)
    leaf(fx, "status", "Show a job", arg("job"))
    leaf(fx, "wait", "Wait for a job to finish (exit 1 if it failed)", arg("job"), timeout)
    leaf(fx, "accept", "Accept a job", arg("job"))
    leaf(fx, "reject", "Reject a job", arg("job"), arg("--reason"))
    leaf(fx, "rollback", "Roll a job back", arg("job"))
    leaf(fx, "review", "Review a project's open pull requests now", arg("project"))
    leaf(fx, "deps", "Queue a dependency update for a project", arg("project"))
    leaf(fx, "tests", "Queue test writing for a project", arg("project"))

    sh = group("shield", "Shield: vibe scans, secrets, pentest, WAF", "shield_cmd")
    leaf(sh, "scan", "Scan a project", arg("--project", required=True), wait, timeout)
    leaf(sh, "findings", "List findings (secrets, or a project's latest scan)", arg("--project"),
         arg("--severity", choices=products.SEVERITIES[1:], help="this level or above"), arg("--status"))
    leaf(sh, "gate", "Scan, wait, and exit 1 if any finding is at or above --fail-on",
         arg("--project", required=True), arg("--fail-on", choices=products.SEVERITIES[1:], default="high"), timeout)
    leaf(sh, "secrets", "Secret Watch sources and finding counts")
    leaf(sh, "pentest", "Run a pentest scan on an enrolled target", arg("target"), wait, timeout)
    leaf(sh, "waf", "WAF and DDoS protected domains")

    op = group("ops", "Ops: servers, incidents, cost, backups", "ops_cmd")
    leaf(op, "status", "On-call summary")
    leaf(op, "incidents", "List incidents", arg("--status", choices=("open", "resolved")))
    leaf(op, "resolve", "Resolve an incident", arg("id"))
    leaf(op, "cost", "Cost recommendations")
    leaf(op, "backup", "Run a backup now", arg("target"))
    leaf(op, "drill", "Run a restore drill", arg("target"))

    md = group("models", "Models: endpoints and fine-tuning", "models_cmd")
    leaf(md, "list", "List AI endpoints and private endpoints")
    leaf(md, "usage", "Endpoint usage", arg("id"), arg("--private", action="store_true", help="a private endpoint"))
    leaf(md, "finetune", "List fine-tune jobs")

    ag = group("agents", "Agents: deploy and run", "agents_cmd")
    leaf(ag, "list", "List agents")
    leaf(ag, "run", "Run an agent (exit 1 if the run failed)", arg("id"), arg("--input", help="JSON, or @file"), wait,
         arg("--timeout", type=float, default=900.0, help="seconds to wait (default 900)"))
    leaf(ag, "runs", "List an agent's runs", arg("id"))

    cp = group("comply", "Comply: frameworks, tasks, audit export", "comply_cmd")
    leaf(cp, "status", "Framework scores and automatic checks")
    leaf(cp, "tasks", "Open and done tasks")
    leaf(cp, "recheck", "Re-run the automatic checks")
    leaf(cp, "export", "Download the audit package", arg("--out", help="file name (default audit-package.zip)"))

    leaf(top, "api", "Call any endpoint", arg("method", help="GET, POST, PATCH, PUT or DELETE"),
         arg("path", help="path under /api/v1/, e.g. fixops/jobs"), arg("--data", help="JSON body, or @file"))
    return root


_HANDLERS = {"fix": cmd_fix, "shield": cmd_shield, "ops": cmd_ops, "models": cmd_models, "agents": cmd_agents,
             "comply": cmd_comply, "api": cmd_api, "whoami": cmd_whoami}


def _resolve_client(args):
    creds = load_credentials()
    key = getattr(args, "key", None) or os.environ.get("LJ_API_KEY") or creds.get("api_key")
    if not key:
        raise UsageError("no API key: use --key, set LJ_API_KEY, or run `lj login`")
    base = getattr(args, "base_url", None) or os.environ.get("LJ_BASE_URL") or creds.get("base_url") or None
    return Client(api_key=key, base_url=base, org=getattr(args, "org", None))


def _login(args, out):
    key = getattr(args, "key", None) or os.environ.get("LJ_API_KEY")
    if not key:
        if sys.stdin.isatty():
            key = getpass.getpass("API key: ")
        else:
            key = sys.stdin.readline()
    key = (key or "").strip()
    if not key:
        raise UsageError("no API key given")
    base = getattr(args, "base_url", None) or os.environ.get("LJ_BASE_URL") or None
    info = None
    if not args.no_verify:
        info = Client(api_key=key, base_url=base).whoami()
    path = save_credentials(key, getattr(args, "base_url", None))
    payload = {"saved": path, "organization": (info or {}).get("organization"), "scopes": (info or {}).get("scopes")}
    out.emit(payload, lambda d: "Saved credentials to %s" % d["saved"] + (
        "\nLogged in to %s with scopes: %s" % ((d["organization"] or {}).get("name"), ", ".join(d["scopes"] or [])) if info else ""))
    return EXIT_OK


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    out = Out(getattr(args, "json", False))
    if not args.cmd:
        parser.print_help()
        return EXIT_USAGE
    sub_dest = args.cmd + "_cmd"
    if args.cmd in PRODUCTS and not getattr(args, sub_dest, None):
        parser.parse_args([args.cmd, "--help"])
    try:
        if args.cmd == "login":
            return _login(args, out)
        if args.cmd == "logout":
            gone = delete_credentials()
            out.emit({"removed": gone}, lambda d: "Removed saved credentials." if d["removed"] else "No saved credentials.")
            return EXIT_OK
        client = _resolve_client(args)
        return _HANDLERS[args.cmd](client, args, out)
    except UsageError as exc:
        out.note("lj: error: %s" % exc)
        return EXIT_USAGE
    except ApiError as exc:
        out.note("lj: error: %s (%s, HTTP %s)" % (exc.message, exc.code, exc.status))
        return EXIT_USAGE
    except TimeoutError as exc:
        out.note("lj: error: timed out: %s" % exc)
        return EXIT_USAGE
    except KeyboardInterrupt:
        out.note("lj: interrupted")
        return 130
    except BrokenPipeError:
        return EXIT_OK


def run():
    sys.exit(main())


if __name__ == "__main__":
    run()
