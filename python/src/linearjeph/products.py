"""LINEARJEPH product SDK client: Fix, Shield, Ops, Models, Agents and Comply. Standard library only.

    from linearjeph import Client
    lj = Client()                                  # LJ_API_KEY and LJ_BASE_URL from the environment
    job = lj.fix.create_job(source_id="gh:acme/app", issue="Checkout 500s when the cart is empty")
    job = lj.fix.wait(job["id"], timeout=1800)
    scan = lj.shield.wait_scan(lj.shield.vibe_scan(12)["id"])
    blocking = lj.shield.gate(scan, fail_on="high")

Every method returns the decoded JSON of the API response (a dict, usually with a "results" list). Errors raise
``ApiError(status, code, message)``. Anything not wrapped is reachable through ``client.request(method, path, ...)``.
"""
import json
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request

from .client import SDK_VERSION

__all__ = ["Client", "ApiError", "SEVERITIES", "severity_rank", "FINAL_JOB", "SETTLED_JOB"]

DEFAULT_BASE_URL = "https://linearjeph.cc"
API_PREFIX = "/api/v1/"
SEVERITIES = ("info", "low", "medium", "high", "critical")  # ascending
FINAL_JOB = ("done", "accepted", "failed", "rejected", "cancelled")  # fixops.models.FINAL
SETTLED_JOB = FINAL_JOB + ("pr_open",)  # pr_open: the fix is delivered and waits for a human decision
FAILED_JOB = ("failed", "rejected", "cancelled")
FINAL_SCAN = ("done", "failed")  # shield.VibeScan / shield.Scan / secscan.Scan
FINAL_RUN = ("succeeded", "failed", "timeout", "cancelled")  # agentcloud.AgentRun
FINAL_FINETUNE = ("succeeded", "failed", "cancelled")  # agentcloud.FineTuneJob
_RETRY_STATUS = (429, 500, 502, 503, 504)


class ApiError(Exception):
    """An error response from the API (or a failed connection, with status 0)."""

    def __init__(self, status, code, message, body=None):
        Exception.__init__(self, "%s (%s, HTTP %s)" % (message, code, status))
        self.status = status
        self.code = code
        self.message = message
        self.body = body

    def __repr__(self):
        return "ApiError(status=%r, code=%r, message=%r)" % (self.status, self.code, self.message)


def severity_rank(name):
    """Position of a severity name, 0 (info) to 4 (critical). Unknown names rank below info."""
    try:
        return SEVERITIES.index(str(name).lower())
    except ValueError:
        return -1


def _qs(**kw):
    return {k: v for k, v in kw.items() if v is not None}


class Client(object):
    """API client. ``Client(api_key=None, base_url=None, org=None, timeout=30)``.

    api_key defaults to $LJ_API_KEY, base_url to $LJ_BASE_URL then https://linearjeph.cc. ``org`` is sent as ``?org=``
    for keys that belong to more than one workspace. GET requests are retried on 429 and 5xx (3 tries, honouring
    Retry-After); writes are never retried.
    """

    def __init__(self, api_key=None, base_url=None, org=None, timeout=30):
        self.api_key = api_key or os.environ.get("LJ_API_KEY") or ""
        self.base_url = (base_url or os.environ.get("LJ_BASE_URL") or DEFAULT_BASE_URL).rstrip("/")
        self.org = org
        self.timeout = float(timeout)
        self.user_agent = "linearjeph-python/" + SDK_VERSION
        self.retries = 3
        self.backoff = 0.5
        self._sleep = time.sleep
        self.fix = _Fix(self)
        self.shield = _Shield(self)
        self.ops = _Ops(self)
        self.models = _Models(self)
        self.agents = _Agents(self)
        self.comply = _Comply(self)

    def __repr__(self):
        return "Client(base_url=%r, org=%r)" % (self.base_url, self.org)

    # ------------------------------------------------------------------ transport
    def _url(self, path, query=None):
        path = str(path)
        if path.startswith("http://") or path.startswith("https://"):
            raise ValueError("pass a path, not a full URL")
        if not path.startswith("/"):
            path = API_PREFIX + path
        elif not (path.startswith("/api/") or path.startswith("/v1/")):
            path = API_PREFIX.rstrip("/") + path
        q = dict(query or {})
        if self.org is not None and "org" not in q:
            q["org"] = self.org
        q = {k: (str(v).lower() if isinstance(v, bool) else v) for k, v in q.items() if v is not None}
        if q:
            path += ("&" if "?" in path else "?") + urllib.parse.urlencode(q)
        return self.base_url + path

    def _send(self, method, path, body=None, query=None):
        """Return (status, headers, raw bytes). Raises ApiError for non-2xx responses."""
        method = method.upper()
        url = self._url(path, query)
        headers = {"Accept": "application/json", "User-Agent": self.user_agent}
        if self.api_key:
            headers["Authorization"] = "Bearer " + self.api_key
        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        tries = self.retries if method == "GET" else 1
        attempt = 0
        while True:
            attempt += 1
            req = urllib.request.Request(url, data=data, method=method, headers=headers)
            try:
                with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                    return resp.status, resp.headers, resp.read()
            except urllib.error.HTTPError as exc:
                raw = exc.read()
                if exc.code in _RETRY_STATUS and attempt < tries:
                    self._sleep(self._delay(exc.headers.get("Retry-After"), attempt))
                    continue
                raise _error_from(exc.code, raw)
            except (urllib.error.URLError, OSError) as exc:
                if attempt < tries:
                    self._sleep(self._delay(None, attempt))
                    continue
                raise ApiError(0, "network_error", "Could not reach %s: %s" % (self.base_url, getattr(exc, "reason", exc)))

    def _delay(self, retry_after, attempt):
        try:
            return max(0.0, min(30.0, float(retry_after)))
        except (TypeError, ValueError):
            return self.backoff * (2 ** (attempt - 1))

    def request(self, method, path, body=None, query=None):
        """Call any endpoint. ``path`` is relative to /api/v1/ ("fixops/jobs") or absolute ("/api/v1/fixops/jobs").
        Returns decoded JSON, None for an empty body, or the text for a non-JSON body."""
        status, headers, raw = self._send(method, path, body, query)
        if not raw:
            return None
        text = raw.decode("utf-8", "replace")
        ctype = (headers.get("Content-Type") or "").lower()
        if "json" in ctype or text[:1] in ("{", "["):
            try:
                return json.loads(text)
            except ValueError:
                pass
        return text

    def download(self, path, query=None):
        """GET a binary or text file (export zips, patches). Returns bytes."""
        return self._send("GET", path, None, query)[2]

    def whoami(self):
        """``{"organization": {"id", "name"}, "scopes": [...], "key": {"prefix", "name", "expires_at"}}``"""
        return self.request("GET", "apikeys/whoami")

    def _poll(self, fetch, is_final, timeout, interval, on_update=None, label="resource"):
        deadline = time.time() + float(timeout)
        last = object()
        while True:
            cur = fetch()
            st = cur.get("status") if isinstance(cur, dict) else None
            if on_update is not None and st != last:
                on_update(cur)
            last = st
            if is_final(cur):
                return cur
            if time.time() >= deadline:
                raise TimeoutError("%s still %r after %ss" % (label, st, timeout))
            self._sleep(max(0.05, float(interval)))


def _error_from(status, raw):
    body = None
    try:
        body = json.loads(raw.decode("utf-8", "replace")) if raw else None
    except ValueError:
        pass
    err = body.get("error") if isinstance(body, dict) else None
    if isinstance(err, dict):
        return ApiError(status, str(err.get("code") or "error"), str(err.get("message") or "Request failed."), body)
    if isinstance(err, str):
        return ApiError(status, "error", err, body)
    snippet = raw.decode("utf-8", "replace").strip()[:200] if raw else ""
    return ApiError(status, "http_%s" % status, snippet or "HTTP %s" % status, body)


class _NS(object):
    def __init__(self, client):
        self._c = client

    def _get(self, path, **query):
        return self._c.request("GET", path, query=_qs(**query))

    def _post(self, path, body=None, **query):
        return self._c.request("POST", path, body if body is not None else {}, _qs(**query))


# ---------------------------------------------------------------------------------------------------------- fix
class _Fix(_NS):
    """AI Fix, code review, dependency updates, test writing and migrations (apps/api-core/fixops)."""

    def overview(self):
        return self._get("fixops/overview")

    def jobs(self, product=None, kind=None, status=None, project=None):
        return self._get("fixops/jobs", product=product, kind=kind, status=status, project=project)

    def create_job(self, source_id, issue, title="", logs="", repro_command="", test_command="", url="", run_on="sandbox",
                   server_ref=""):
        return self._post("fixops/jobs", {"source_id": source_id, "title": title, "issue": issue, "logs": logs,
                                          "repro_command": repro_command, "test_command": test_command, "url": url,
                                          "run_on": run_on, "server_ref": server_ref})

    def job(self, job_id):
        return self._get("fixops/jobs/%d" % int(job_id))

    def events(self, job_id, after=0):
        return self._get("fixops/jobs/%d/events" % int(job_id), after=after or None)

    def cancel(self, job_id):
        return self._post("fixops/jobs/%d/cancel" % int(job_id))

    def accept(self, job_id):
        return self._post("fixops/jobs/%d/accept" % int(job_id))

    def reject(self, job_id, reason=""):
        return self._post("fixops/jobs/%d/reject" % int(job_id), {"reason": reason})

    def rollback(self, job_id):
        return self._post("fixops/jobs/%d/rollback" % int(job_id))

    def patch(self, job_id):
        """The job's unified diff as text."""
        return self._c.download("fixops/jobs/%d/patch" % int(job_id)).decode("utf-8", "replace")

    def projects(self):
        return self._get("fixops/projects")

    def project(self, project_id):
        return self._get("fixops/projects/%d" % int(project_id))

    def create_project(self, source_id, **settings):
        body = dict(settings)
        body["source_id"] = source_id
        return self._post("fixops/projects", body)

    def update_project(self, project_id, **settings):
        return self._c.request("PATCH", "fixops/projects/%d" % int(project_id), settings)

    def review(self, project_id):
        """Poll the project's pull requests for review now (POST review-now)."""
        return self._post("fixops/projects/%d/review-now" % int(project_id))

    def deps(self, project_id):
        """Queue a dependency-update job for the project (POST deps-now)."""
        return self._post("fixops/projects/%d/deps-now" % int(project_id))

    def tests(self, project_id, max_files=5):
        """Queue a test-writing job for the project (POST tests-now)."""
        return self._post("fixops/projects/%d/tests-now" % int(project_id), {"max_files": int(max_files)})

    def migrations(self):
        return self._get("fixops/migrations")

    def migration(self, migration_id):
        return self._get("fixops/migrations/%d" % int(migration_id))

    def plan(self, migration_id):
        """Generate (or regenerate) the migration plan."""
        return self._post("fixops/migrations/%d/plan" % int(migration_id))

    def wait(self, job_id, timeout=1800, interval=3.0, on_update=None, until=SETTLED_JOB):
        """Poll until the job reaches a final status (done, accepted, failed, rejected, cancelled) or a pull request is
        open (``pr_open``, which waits for a human). Returns the job. Raises TimeoutError."""
        return self._c._poll(lambda: self.job(job_id), lambda j: j.get("status") in until, timeout, interval,
                             on_update, "job %s" % job_id)


# -------------------------------------------------------------------------------------------------------- shield
class _Shield(_NS):
    """Vibe Rescue, Secret Watch, Pentest, WAF, Phishing and site security scans (apps/api-core/shield, secscan)."""

    def overview(self):
        return self._get("shield/overview")

    # Vibe Rescue
    def vibe_projects(self):
        return self._get("shield/vibe/projects")

    def vibe_scan(self, project_id):
        """Queue a scan of a Vibe Rescue project. Returns the scan row (id, status, ...)."""
        return self._post("shield/vibe/projects/%d/scan" % int(project_id))

    def vibe_scan_detail(self, scan_id):
        return self._get("shield/vibe/scans/%d" % int(scan_id))

    # Secret Watch
    def secrets_overview(self):
        return self._get("shield/secrets")

    def secrets_add_source(self, kind, ref):
        return self._post("shield/secrets/sources", {"kind": kind, "ref": ref})

    def secrets_scan(self, source_id):
        return self._post("shield/secrets/sources/%d/scan" % int(source_id))

    def secrets_findings(self, status=None):
        return self._get("shield/secrets/findings", status=status)

    # Pentest
    def pentest_overview(self):
        return self._get("shield/pentest")

    def pentest_scan(self, target_id):
        return self._post("shield/pentest/targets/%d/scan" % int(target_id))

    def pentest_scan_detail(self, scan_id):
        return self._get("shield/pentest/scans/%d" % int(scan_id))

    def pentest_history(self, target_id):
        return self._get("shield/pentest/targets/%d/history" % int(target_id))

    def pentest_retest(self, finding_id):
        return self._post("shield/pentest/findings/%d/retest" % int(finding_id))

    # WAF
    def waf_overview(self):
        return self._get("shield/waf")

    def waf_events(self, domain_id):
        return self._get("shield/waf/domains/%d/events" % int(domain_id))

    # Phishing simulation
    def phishing_overview(self):
        return self._get("shield/phishing")

    def phishing_campaign(self, campaign_id):
        return self._get("shield/phishing/campaigns/%d" % int(campaign_id))

    # Site security scans (/api/v1/security/)
    def security_overview(self):
        return self._get("security/overview")

    def security_scan(self, target_id):
        return self._post("security/scans", {"target": int(target_id)})

    def security_scan_detail(self, scan_id):
        return self._get("security/scans/%d" % int(scan_id))

    # helpers
    def wait_scan(self, scan_id, kind="vibe", timeout=900, interval=3.0, on_update=None):
        """Poll a scan until it is done or failed. kind: "vibe" (default), "pentest" or "security"."""
        fetch = {"vibe": self.vibe_scan_detail, "pentest": self.pentest_scan_detail,
                 "security": self.security_scan_detail}[kind]
        return self._c._poll(lambda: fetch(scan_id), lambda s: s.get("status") in FINAL_SCAN, timeout, interval,
                             on_update, "scan %s" % scan_id)

    @staticmethod
    def gate(scan, fail_on="high"):
        """Findings of a scan detail (dict with "findings") at or above ``fail_on`` (low, medium, high or critical).
        An empty list means the gate passes. Status "failed" scans are the caller's call (see ``scan["status"]``)."""
        floor = severity_rank(fail_on)
        if floor < 0:
            raise ValueError("fail_on must be one of %s" % ", ".join(SEVERITIES[1:]))
        return [f for f in (scan.get("findings") or []) if severity_rank(f.get("severity")) >= floor]


# ---------------------------------------------------------------------------------------------------------- ops
class _Ops(_NS):
    """On-call agent, incidents, cost cutter, managed databases and backup drills (opsagent, backups)."""

    def summary(self):
        return self._get("opsagent/summary")

    def servers(self):
        return self._get("opsagent/servers")

    def incidents(self, status=None):
        return self._get("opsagent/incidents", status=status)

    def incident(self, incident_id):
        return self._get("opsagent/incidents/%d" % int(incident_id))

    def resolve(self, incident_id):
        return self._post("opsagent/incidents/%d/resolve" % int(incident_id))

    def cost(self):
        return self._get("opsagent/cost")

    def cost_analyse(self):
        return self._post("opsagent/cost/analyse")

    def managed_databases(self):
        return self._get("opsagent/managed-db")

    def backup_targets(self):
        return self._get("backups/agent/targets")

    def backup_run(self, target_id):
        return self._post("backups/agent/targets/%d/run" % int(target_id))

    def backup_drill(self, target_id):
        return self._post("backups/agent/targets/%d/drill" % int(target_id))

    def backup_runs(self, target_id):
        return self._get("backups/agent/targets/%d/runs" % int(target_id))


# -------------------------------------------------------------------------------------------------------- models
class _Models(_NS):
    """AI Endpoints (serverless), private endpoints and fine-tuning (serverless, agentcloud/private, agentcloud/finetune)."""

    def endpoints(self):
        return self._get("endpoints/endpoints")

    def endpoint(self, endpoint_id):
        return self._get("endpoints/endpoints/%d" % int(endpoint_id))

    def usage(self, endpoint_id):
        return self._get("endpoints/endpoints/%d/usage" % int(endpoint_id))

    def action(self, endpoint_id, action):
        """action: suspend, resume or rotate-key (the new key is returned once)."""
        return self._post("endpoints/endpoints/%d/%s" % (int(endpoint_id), _slug(action)))

    def private_endpoints(self):
        return self._get("agentcloud/private/endpoints")

    def private_endpoint(self, endpoint_id):
        return self._get("agentcloud/private/endpoints/%d" % int(endpoint_id))

    def private_usage(self, endpoint_id):
        return self._get("agentcloud/private/endpoints/%d/usage" % int(endpoint_id))

    def private_logs(self, endpoint_id):
        return self._get("agentcloud/private/endpoints/%d/logs" % int(endpoint_id))

    def finetune_datasets(self):
        return self._get("agentcloud/finetune/datasets")

    def finetune_jobs(self):
        return self._get("agentcloud/finetune/jobs")

    def finetune_job(self, job_id):
        return self._get("agentcloud/finetune/jobs/%d" % int(job_id))

    def finetune_wait(self, job_id, timeout=3600, interval=10.0, on_update=None):
        return self._c._poll(lambda: self.finetune_job(job_id), lambda j: j.get("status") in FINAL_FINETUNE, timeout,
                             interval, on_update, "fine-tune job %s" % job_id)


# -------------------------------------------------------------------------------------------------------- agents
class _Agents(_NS):
    """Agent Cloud (apps/api-core/agentcloud)."""

    def overview(self):
        return self._get("agentcloud/overview")

    def list(self):
        return self._get("agentcloud/agents")

    def get(self, agent_id):
        return self._get("agentcloud/agents/%d" % int(agent_id))

    def runs(self, agent_id):
        return self._get("agentcloud/agents/%d/runs" % int(agent_id))

    def run_detail(self, agent_id, run_id):
        return self._get("agentcloud/agents/%d/runs/%d" % (int(agent_id), int(run_id)))

    def action(self, agent_id, action, input=None):
        """action: run, pause, resume, redeploy or rotate-webhook. ``input`` is only used by run."""
        body = {"input": input} if (action == "run" and input is not None) else {}
        return self._post("agentcloud/agents/%d/%s" % (int(agent_id), _slug(action)), body)

    def run(self, agent_id, input=None):
        return self.action(agent_id, "run", input)

    def pause(self, agent_id):
        return self.action(agent_id, "pause")

    def resume(self, agent_id):
        return self.action(agent_id, "resume")

    def wait_run(self, agent_id, run_id, timeout=900, interval=2.0, on_update=None):
        return self._c._poll(lambda: self.run_detail(agent_id, run_id), lambda r: r.get("status") in FINAL_RUN, timeout,
                             interval, on_update, "run %s" % run_id)


# -------------------------------------------------------------------------------------------------------- comply
class _Comply(_NS):
    """Compliance automation (apps/api-core/comply)."""

    def overview(self):
        return self._get("comply/overview")

    def compliance(self):
        """Framework scores, automatic checks, available frameworks, policies and open task count."""
        return self._get("comply/compliance")

    def frameworks(self):
        return self.compliance().get("available") or []

    def framework(self, key):
        return self._get("comply/compliance/frameworks/%s" % _slug(key))

    def enable_framework(self, key):
        return self._post("comply/compliance/frameworks", {"key": key})

    def tasks(self):
        return self._get("comply/compliance/tasks")

    def create_task(self, title, control=None, due_date=None, owner=None):
        return self._post("comply/compliance/tasks", {k: v for k, v in (("title", title), ("control", control),
                                                                    ("due_date", due_date), ("owner", owner)) if v is not None})

    def update_task(self, task_id, **fields):
        return self._c.request("PATCH", "comply/compliance/tasks/%d" % int(task_id), fields)

    def recheck(self):
        return self._post("comply/compliance/recheck")

    def export(self):
        """The audit package (zip) as bytes."""
        return self._c.download("comply/compliance/export")


_SLUG = re.compile(r"^[A-Za-z0-9._-]+$")


def _slug(value):
    value = str(value)
    if not _SLUG.match(value):
        raise ValueError("invalid path segment: %r" % value)
    return value
