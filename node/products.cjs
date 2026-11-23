"use strict";
/**
 * LINEARJEPH product API client: Fix, Shield, Ops, Models, Agents and Comply.
 * Zero dependencies (global fetch, Node >= 18). Authenticates with an API key (ljk_...) created in the dashboard.
 * Every method maps to one route in apps/api-core (see docs/product-sdk-contract.md); see ROUTES at the bottom.
 */
const SDK_VERSION = "1.3.0";
const DEFAULT_BASE_URL = "https://linearjeph.cc";

const SEVERITIES = ["info", "low", "medium", "high", "critical"];
const FIX_FINAL = ["done", "accepted", "failed", "rejected", "cancelled"];
const FIX_BAD = ["failed", "rejected", "cancelled"];
const SCAN_FINAL = ["done", "failed"];
const RUN_FINAL = ["succeeded", "failed", "timeout", "cancelled"];
const FT_FINAL = ["succeeded", "failed", "cancelled"];
const BACKUP_FINAL = ["success", "failed"];

class ApiError extends Error {
  constructor(status, code, message, body) {
    super(message || code || "request failed");
    this.name = "ApiError";
    this.status = status; this.code = code || "error"; this.body = body;
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const q = (v) => encodeURIComponent(String(v));
const id = (v, what) => {
  const n = typeof v === "string" && /^\d+$/.test(v) ? Number(v) : v;
  if (!Number.isInteger(n) || n < 0) throw new ApiError(0, "invalid", `${what || "id"} must be a whole number`);
  return n;
};

class Client {
  /**
   * @param {{apiKey?:string, baseUrl?:string, org?:string|number, timeout?:number, retries?:number, backoffMs?:number, fetch?:Function}|string} [opts]
   */
  constructor(opts) {
    const o = typeof opts === "string" ? { apiKey: opts } : opts || {};
    const env = typeof process !== "undefined" && process.env ? process.env : {};
    this.apiKey = o.apiKey || env.LJ_API_KEY || "";
    this.baseUrl = String(o.baseUrl || env.LJ_BASE_URL || DEFAULT_BASE_URL).replace(/\/+$/, "");
    this.org = o.org == null ? null : o.org;
    this.timeout = o.timeout || 30;          // seconds
    this.retries = o.retries === undefined ? 3 : o.retries;   // total tries for GET
    this.backoffMs = o.backoffMs === undefined ? 500 : o.backoffMs;
    this._fetch = o.fetch || null;
    Object.defineProperty(this, "apiKey", { enumerable: false, writable: true, value: this.apiKey });  // never serialised or logged
    this.fix = fixApi(this); this.shield = shieldApi(this); this.ops = opsApi(this);
    this.models = modelsApi(this); this.agents = agentsApi(this); this.comply = complyApi(this);
  }

  /**
   * Call any route. `path` is relative to /api/v1/ ("fixops/jobs") or absolute ("/api/v1/fixops/jobs").
   * Resolves to parsed JSON, a string (text), a Buffer (other types) or null (204).
   */
  async request(method, path, body, query) {
    method = String(method || "GET").toUpperCase();
    if (!this.apiKey) throw new ApiError(0, "no_key", "No API key. Set LJ_API_KEY or pass apiKey.");
    let p = String(path || "");
    if (!/^\/api\//.test(p)) p = "/api/v1/" + p.replace(/^\/+/, "");
    const params = new URLSearchParams();
    const all = { ...(this.org != null ? { org: this.org } : {}), ...(query || {}) };
    for (const [k, v] of Object.entries(all)) if (v !== undefined && v !== null && v !== "") params.append(k, String(v));
    const url = this.baseUrl + p + (params.toString() ? (p.includes("?") ? "&" : "?") + params.toString() : "");
    const headers = { Authorization: "Bearer " + this.apiKey, Accept: "application/json", "User-Agent": "linearjeph-node/" + SDK_VERSION };
    let payload;
    if (body !== undefined && body !== null && method !== "GET" && method !== "HEAD") { payload = JSON.stringify(body); headers["Content-Type"] = "application/json"; }
    const doFetch = this._fetch || fetch;
    const tries = method === "GET" ? Math.max(1, this.retries) : 1;
    let last;
    for (let i = 0; i < tries; i++) {
      let res;
      try {
        res = await doFetch(url, { method, headers, body: payload, signal: AbortSignal.timeout(this.timeout * 1000) });
      } catch (e) {
        last = new ApiError(0, e && e.name === "TimeoutError" ? "timeout" : "network", "Could not reach " + this.baseUrl + ": " + (e && e.message));
        if (i + 1 < tries) { await sleep(this.backoffMs * 2 ** i); continue; }
        throw last;
      }
      const err = await parse(res);
      if (res.ok) return err.value;
      last = errorFrom(res.status, err.value);
      if (i + 1 < tries && (res.status === 429 || res.status >= 500)) {
        const ra = Number(res.headers.get("retry-after"));
        await sleep(Number.isFinite(ra) && ra > 0 ? Math.min(ra, 30) * 1000 : this.backoffMs * 2 ** i);
        continue;
      }
      throw last;
    }
    throw last;
  }

  /** Who this key is: organization, scopes and key metadata. */
  whoami() { return this.request("GET", "apikeys/whoami"); }

  /** Poll `fn` until `done(result)` is true. Rejects with ApiError(code "timeout") after `timeout` seconds. */
  async _poll(fn, done, { timeout = 600, interval = 3, onUpdate } = {}) {
    const end = Date.now() + timeout * 1000;
    for (;;) {
      const r = await fn();
      if (onUpdate) { try { onUpdate(r); } catch (_) { /* ignore */ } }
      if (done(r)) return r;
      if (Date.now() + interval * 1000 > end) throw new ApiError(0, "timeout", `Still not finished after ${timeout} seconds`);
      await sleep(interval * 1000);
    }
  }
}

async function parse(res) {
  if (res.status === 204) return { value: null };
  const ct = res.headers.get("content-type") || "";
  if (/json/i.test(ct)) { const t = await res.text(); try { return { value: t ? JSON.parse(t) : null }; } catch (_) { return { value: t }; } }
  if (/^text\//i.test(ct)) return { value: await res.text() };
  return { value: Buffer.from(await res.arrayBuffer()) };
}
function errorFrom(status, v) {
  const e = v && typeof v === "object" ? v.error : null;
  if (e && typeof e === "object") return new ApiError(status, e.code, e.message, v);
  if (typeof e === "string") return new ApiError(status, "error", e, v);
  const msg = v && typeof v === "object" && (v.detail || v.message);
  return new ApiError(status, status === 401 ? "unauthorized" : status === 403 ? "forbidden" : status === 402 ? "not_subscribed" : "error",
    msg || (typeof v === "string" && v.slice(0, 200)) || `HTTP ${status}`, v);
}

/** Findings (array of {severity}) at or above `failOn`. Accepts a scan detail, a findings array or { findings }. */
function gateFindings(scan, failOn = "high") {
  const min = SEVERITIES.indexOf(String(failOn).toLowerCase());
  if (min < 0) throw new ApiError(0, "invalid", `failOn must be one of ${SEVERITIES.slice(1).join(", ")}`);
  const list = Array.isArray(scan) ? scan : (scan && scan.findings) || [];
  return list.filter((f) => SEVERITIES.indexOf(String(f.severity).toLowerCase()) >= min);
}

// ------------------------------------------------------------------------------------------------ fix (fixops)
function fixApi(c) {
  const g = (p, qy) => c.request("GET", p, undefined, qy), p = (path, body) => c.request("POST", path, body || {});
  const api = {
    overview: () => g("fixops/overview"),
    jobs: {
      list: (qy) => g("fixops/jobs", qy),                       // { project, kind, status, product }
      create: (body) => p("fixops/jobs", body),               // { source_id, title, issue|logs|repro_command|url, test_command, run_on, server_ref }
      get: (job) => g(`fixops/jobs/${id(job)}`),
      events: (job, after) => g(`fixops/jobs/${id(job)}/events`, after ? { after } : undefined),
      cancel: (job) => p(`fixops/jobs/${id(job)}/cancel`),
      accept: (job) => p(`fixops/jobs/${id(job)}/accept`),
      reject: (job, reason) => p(`fixops/jobs/${id(job)}/reject`, reason ? { reason } : {}),
      rollback: (job) => p(`fixops/jobs/${id(job)}/rollback`),
      patch: (job) => g(`fixops/jobs/${id(job)}/patch`),     // unified diff as a string
    },
    projects: {
      list: () => g("fixops/projects"),
      get: (project) => g(`fixops/projects/${id(project)}`),
      update: (project, body) => c.request("PATCH", `fixops/projects/${id(project)}`, body),
      review: (project) => p(`fixops/projects/${id(project)}/review-now`),
      deps: (project) => p(`fixops/projects/${id(project)}/deps-now`),
      tests: (project, maxFiles) => p(`fixops/projects/${id(project)}/tests-now`, maxFiles ? { max_files: maxFiles } : {}),
    },
    migrations: {
      list: () => g("fixops/migrations"),
      get: (m) => g(`fixops/migrations/${id(m)}`),
      plan: (m) => p(`fixops/migrations/${id(m)}/plan`),
    },
    /**
     * Poll a job until it is finished: done, accepted, failed, rejected or cancelled, or a pull request is open
     * (pr_open: waiting for a human). Resolves to the job; check `job.status`.
     */
    wait: (job, opts = {}) => c._poll(() => api.jobs.get(job), (j) => FIX_FINAL.includes(j.status) || j.status === "pr_open", opts),
    /** Create a fix job for a project: looks up the project's source, then POSTs the job. */
    async run(project, { issue, title, ...rest } = {}) {
      const proj = /^\d+$/.test(String(project)) ? await api.projects.get(project) : { source_id: String(project) };   // digits: project id; otherwise a source id
      const t = title || String(issue || rest.logs || rest.repro_command || "Fix").split("\n")[0].trim().slice(0, 120) || "Fix";
      return api.jobs.create({ source_id: proj.source_id, title: t, issue, ...rest });
    },
  };
  return api;
}

// ------------------------------------------------------------------------------------------------ shield
function shieldApi(c) {
  const g = (p, qy) => c.request("GET", p, undefined, qy), p = (path, body) => c.request("POST", path, body || {});
  const scanWait = (get) => (scan, opts = {}) => c._poll(() => get(scan), (s) => SCAN_FINAL.includes(s.status), opts);
  const api = {
    overview: () => g("shield/overview"),
    vibe: {
      projects: () => g("shield/vibe/projects"),
      scan: (project) => p(`shield/vibe/projects/${id(project)}/scan`),
      scanDetail: (scan) => g(`shield/vibe/scans/${id(scan)}`),
      wait: (scan, opts) => scanWait(api.vibe.scanDetail)(scan, opts),
    },
    secrets: {
      overview: () => g("shield/secrets"),
      sources: async () => (await g("shield/secrets")).sources || [],
      scan: (source) => p(`shield/secrets/sources/${id(source)}/scan`),
      findings: (qy) => g("shield/secrets/findings", qy),    // { status: open|acknowledged|revoked|false_positive }
    },
    pentest: {
      overview: () => g("shield/pentest"),
      scan: (target) => p(`shield/pentest/targets/${id(target)}/scan`),
      scanDetail: (scan) => g(`shield/pentest/scans/${id(scan)}`),
      history: (target) => g(`shield/pentest/targets/${id(target)}/history`),
      retest: (finding) => p(`shield/pentest/findings/${id(finding)}/retest`),
      wait: (scan, opts) => scanWait(api.pentest.scanDetail)(scan, opts),
    },
    waf: {
      overview: () => g("shield/waf"),
      events: (domain) => g(`shield/waf/domains/${id(domain)}/events`),
    },
    phishing: {
      overview: () => g("shield/phishing"),
      campaigns: async () => (await g("shield/phishing")).campaigns || [],
      campaign: (campaign) => g(`shield/phishing/campaigns/${id(campaign)}`),
    },
    security: {
      overview: () => g("security/overview"),
      scans: async () => (await g("security/overview")).scans || [],
      scan: (scan) => g(`security/scans/${id(scan)}`),
      start: (target) => p("security/scans", { target: id(target, "target") }),
      wait: (scan, opts) => scanWait(api.security.scan)(scan, opts),
    },
    /** Findings at or above `failOn` (low, medium, high, critical). `scan` is a scan detail or a findings array. */
    gate: (scan, failOn = "high") => gateFindings(scan, failOn),
    /** Scan a Vibe Rescue project, wait for it and apply the gate. Resolves to { scan, failing, passed }. */
    async gateProject(project, { failOn = "high", wait = true, ...opts } = {}) {
      gateFindings([], failOn);
      const queued = await api.vibe.scan(project);
      const scan = wait ? await api.vibe.wait(queued.id, opts) : await api.vibe.scanDetail(queued.id);
      const failing = scan.status === "done" ? gateFindings(scan, failOn) : [];
      return { scan, failing, passed: scan.status === "done" && failing.length === 0 };
    },
  };
  return api;
}

// ------------------------------------------------------------------------------------------------ ops
function opsApi(c) {
  const g = (p, qy) => c.request("GET", p, undefined, qy), p = (path, body) => c.request("POST", path, body || {});
  const api = {
    summary: () => g("opsagent/summary"),
    servers: () => g("opsagent/servers"),
    incidents: {
      list: (qy) => g("opsagent/incidents", qy),             // { status: open|resolved }
      get: (inc) => g(`opsagent/incidents/${id(inc)}`),
      resolve: (inc) => p(`opsagent/incidents/${id(inc)}/resolve`),
    },
    cost: {
      summary: () => g("opsagent/cost"),
      analyse: () => p("opsagent/cost/analyse"),
    },
    managedDatabases: { list: () => g("opsagent/managed-db") },
    backups: {
      targets: () => g("backups/agent/targets"),
      run: (target) => p(`backups/agent/targets/${id(target)}/run`),
      drill: (target) => p(`backups/agent/targets/${id(target)}/drill`),
      runs: (target) => g(`backups/agent/targets/${id(target)}/runs`),   // { backups, drills }
    },
  };
  return api;
}

// ------------------------------------------------------------------------------------------------ models
function modelsApi(c) {
  const g = (p, qy) => c.request("GET", p, undefined, qy), p = (path, body) => c.request("POST", path, body || {});
  return {
    endpoints: {
      list: () => g("endpoints/endpoints"),
      get: (ep) => g(`endpoints/endpoints/${id(ep)}`),
      usage: (ep) => g(`endpoints/endpoints/${id(ep)}/usage`),
      action: (ep, action) => p(`endpoints/endpoints/${id(ep)}/${q(action)}`),   // suspend | resume | rotate-key
    },
    private: {
      list: () => g("agentcloud/private/endpoints"),
      get: (ep) => g(`agentcloud/private/endpoints/${id(ep)}`),
      usage: (ep) => g(`agentcloud/private/endpoints/${id(ep)}/usage`),
      logs: (ep) => g(`agentcloud/private/endpoints/${id(ep)}/logs`),
    },
    finetune: {
      datasets: () => g("agentcloud/finetune/datasets"),
      jobs: () => g("agentcloud/finetune/jobs"),
      job: (job) => g(`agentcloud/finetune/jobs/${id(job)}`),
      createJob: (body) => p("agentcloud/finetune/jobs", body),   // { dataset_id, base_model, method, epochs, learning_rate, lora_rank, name }
      jobAction: (job, action, body) => p(`agentcloud/finetune/jobs/${id(job)}/${q(action)}`, body),   // cancel | deploy
    },
  };
}

// ------------------------------------------------------------------------------------------------ agents
function agentsApi(c) {
  const g = (p, qy) => c.request("GET", p, undefined, qy), p = (path, body) => c.request("POST", path, body || {});
  const api = {
    overview: () => g("agentcloud/overview"),
    list: () => g("agentcloud/agents"),
    get: (agent) => g(`agentcloud/agents/${id(agent)}`),
    runs: (agent) => g(`agentcloud/agents/${id(agent)}/runs`),
    runDetail: (agent, run) => g(`agentcloud/agents/${id(agent)}/runs/${id(run, "run")}`),
    cancelRun: (agent, run) => p(`agentcloud/agents/${id(agent)}/runs/${id(run, "run")}`),
    /** run | pause | resume | redeploy | rotate-webhook */
    action: (agent, action, body) => p(`agentcloud/agents/${id(agent)}/${q(action)}`, body),
    run: (agent, input) => api.action(agent, "run", input === undefined ? {} : { input }),
    waitRun: (agent, run, opts) => c._poll(() => api.runDetail(agent, run), (r) => RUN_FINAL.includes(r.status), opts),
  };
  return api;
}

// ------------------------------------------------------------------------------------------------ comply
function complyApi(c) {
  const g = (p, qy) => c.request("GET", p, undefined, qy), p = (path, body) => c.request("POST", path, body || {});
  return {
    overview: () => g("comply/overview"),
    compliance: () => g("comply/compliance"),
    frameworks: async () => (await g("comply/compliance")).frameworks || [],
    framework: (key) => g(`comply/compliance/frameworks/${q(key)}`),
    tasks: {
      list: async () => g("comply/compliance/tasks"),
      create: (body) => p("comply/compliance/tasks", body),
      update: (task, body) => c.request("PATCH", `comply/compliance/tasks/${id(task)}`, body),
    },
    recheck: () => p("comply/compliance/recheck"),
    /** The audit package as a Buffer (zip). */
    export: () => g("comply/compliance/export"),
  };
}

const ROUTES = {
  "fix.overview": "GET fixops/overview", "fix.jobs.list": "GET fixops/jobs", "fix.jobs.create": "POST fixops/jobs",
  "fix.jobs.get": "GET fixops/jobs/{id}", "fix.jobs.events": "GET fixops/jobs/{id}/events", "fix.jobs.cancel": "POST fixops/jobs/{id}/cancel",
  "fix.jobs.accept": "POST fixops/jobs/{id}/accept", "fix.jobs.reject": "POST fixops/jobs/{id}/reject", "fix.jobs.rollback": "POST fixops/jobs/{id}/rollback",
  "fix.jobs.patch": "GET fixops/jobs/{id}/patch", "fix.projects.list": "GET fixops/projects", "fix.projects.get": "GET fixops/projects/{id}",
  "fix.projects.update": "PATCH fixops/projects/{id}", "fix.projects.review": "POST fixops/projects/{id}/review-now",
  "fix.projects.deps": "POST fixops/projects/{id}/deps-now", "fix.projects.tests": "POST fixops/projects/{id}/tests-now",
  "fix.migrations.list": "GET fixops/migrations", "fix.migrations.get": "GET fixops/migrations/{id}", "fix.migrations.plan": "POST fixops/migrations/{id}/plan",
  "shield.overview": "GET shield/overview", "shield.vibe.projects": "GET shield/vibe/projects", "shield.vibe.scan": "POST shield/vibe/projects/{id}/scan",
  "shield.vibe.scanDetail": "GET shield/vibe/scans/{id}", "shield.secrets.overview": "GET shield/secrets", "shield.secrets.scan": "POST shield/secrets/sources/{id}/scan",
  "shield.secrets.findings": "GET shield/secrets/findings", "shield.pentest.overview": "GET shield/pentest", "shield.pentest.scan": "POST shield/pentest/targets/{id}/scan",
  "shield.pentest.scanDetail": "GET shield/pentest/scans/{id}", "shield.pentest.history": "GET shield/pentest/targets/{id}/history",
  "shield.pentest.retest": "POST shield/pentest/findings/{id}/retest", "shield.waf.overview": "GET shield/waf", "shield.waf.events": "GET shield/waf/domains/{id}/events",
  "shield.phishing.overview": "GET shield/phishing", "shield.phishing.campaign": "GET shield/phishing/campaigns/{id}",
  "shield.security.overview": "GET security/overview", "shield.security.scan": "GET security/scans/{id}", "shield.security.start": "POST security/scans",
  "ops.summary": "GET opsagent/summary", "ops.servers": "GET opsagent/servers", "ops.incidents.list": "GET opsagent/incidents",
  "ops.incidents.get": "GET opsagent/incidents/{id}", "ops.incidents.resolve": "POST opsagent/incidents/{id}/resolve", "ops.cost.summary": "GET opsagent/cost",
  "ops.cost.analyse": "POST opsagent/cost/analyse", "ops.managedDatabases.list": "GET opsagent/managed-db", "ops.backups.targets": "GET backups/agent/targets",
  "ops.backups.run": "POST backups/agent/targets/{id}/run", "ops.backups.drill": "POST backups/agent/targets/{id}/drill", "ops.backups.runs": "GET backups/agent/targets/{id}/runs",
  "models.endpoints.list": "GET endpoints/endpoints", "models.endpoints.get": "GET endpoints/endpoints/{id}", "models.endpoints.usage": "GET endpoints/endpoints/{id}/usage",
  "models.endpoints.action": "POST endpoints/endpoints/{id}/{suspend|resume|rotate-key}", "models.private.list": "GET agentcloud/private/endpoints",
  "models.private.get": "GET agentcloud/private/endpoints/{id}", "models.private.usage": "GET agentcloud/private/endpoints/{id}/usage",
  "models.private.logs": "GET agentcloud/private/endpoints/{id}/logs", "models.finetune.datasets": "GET agentcloud/finetune/datasets",
  "models.finetune.jobs": "GET agentcloud/finetune/jobs", "models.finetune.job": "GET agentcloud/finetune/jobs/{id}",
  "models.finetune.createJob": "POST agentcloud/finetune/jobs", "models.finetune.jobAction": "POST agentcloud/finetune/jobs/{id}/{cancel|deploy}",
  "agents.overview": "GET agentcloud/overview", "agents.list": "GET agentcloud/agents", "agents.get": "GET agentcloud/agents/{id}",
  "agents.runs": "GET agentcloud/agents/{id}/runs", "agents.runDetail": "GET agentcloud/agents/{id}/runs/{run}", "agents.cancelRun": "POST agentcloud/agents/{id}/runs/{run}",
  "agents.action": "POST agentcloud/agents/{id}/{run|pause|resume|redeploy|rotate-webhook}",
  "comply.overview": "GET comply/overview", "comply.compliance": "GET comply/compliance", "comply.framework": "GET comply/compliance/frameworks/{key}",
  "comply.tasks.list": "GET comply/compliance/tasks", "comply.tasks.create": "POST comply/compliance/tasks", "comply.tasks.update": "PATCH comply/compliance/tasks/{id}",
  "comply.recheck": "POST comply/compliance/recheck", "comply.export": "GET comply/compliance/export",
  whoami: "GET apikeys/whoami",
};

module.exports = { Client, ApiError, SDK_VERSION, DEFAULT_BASE_URL, SEVERITIES, FIX_FINAL, FIX_BAD, SCAN_FINAL, RUN_FINAL, FT_FINAL, BACKUP_FINAL, gateFindings, ROUTES };
