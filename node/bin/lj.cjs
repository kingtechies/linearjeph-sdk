#!/usr/bin/env node
"use strict";
/**
 * lj: command line for the LINEARJEPH product APIs (Fix, Shield, Ops, Models, Agents, Comply).
 * Key: --key, then LJ_API_KEY, then ~/.config/linearjeph/credentials (written by `lj login`, mode 0600, never printed).
 * Exit codes: 0 ok, 1 gate failed / job or scan failed, 2 usage, auth, API or network error.
 */
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { Client, ApiError, SDK_VERSION, SEVERITIES, FIX_BAD, gateFindings } = require("../products.cjs");

// ------------------------------------------------------------------------------------------------ help
const HELP = {
  _: `lj ${SDK_VERSION}: command line for LINEARJEPH Fix, Shield, Ops, Models, Agents and Comply

usage: lj <command> [options]

  login [--key KEY]       save an API key (reads LJ_API_KEY or stdin when --key is omitted)
  logout                  delete the saved key
  whoami                  show the organization and scopes of the key
  fix <cmd>               automated fixes, code review, dependency updates, tests
  shield <cmd>            security scans, secrets, pentest, WAF
  ops <cmd>               servers, incidents, cost, backups and restore drills
  models <cmd>            AI endpoints and fine-tuning
  agents <cmd>            hosted agents
  comply <cmd>            compliance status, tasks and the audit package
  api <METHOD> <path> [--data json]   call any route under /api/v1/

Run "lj <product> --help" for the commands of one product.

global options:
  --key KEY        API key (default: LJ_API_KEY, then the saved credentials)
  --base-url URL   server (default: LJ_BASE_URL, then https://linearjeph.cc)
  --org ID         organization when the key can see several
  --json           print raw JSON instead of tables
  -h, --help       this help

exit codes: 0 ok, 1 gate failed / job or scan failed, 2 usage, auth, API or network error`,
  fix: `lj fix <command>

  jobs [--project ID] [--status S]          list fix jobs
  run --project ID|SOURCE --issue "text" [--title T] [--test-command C] [--wait]
                                            start a fix job (--wait: poll until it finishes or a PR is open)
  status <job>                              show one job
  wait <job> [--timeout SEC]                wait for a job; exit 1 if it failed, was rejected or cancelled
  accept|reject|rollback <job> [--reason R] decide on a job
  review <project>                          review open pull requests now
  deps <project>                            start a dependency update
  tests <project> [--max-files N]           start test writing`,
  shield: `lj shield <command>

  scan --project ID [--wait]                scan a Vibe Rescue project
  findings [--project ID] [--scan ID] [--severity high]
                                            findings at or above a severity (latest scan of the project; leaked-secret findings without --project)
  gate --project ID --fail-on <low|medium|high|critical> [--timeout SEC]
                                            scan, wait, exit 1 if any finding is at or above the severity
  secrets                                   leaked-secret sources and counts
  pentest <target> [--wait] [--fail-on S]   run a pentest scan on an enrolled site
  waf                                       protected domains`,
  ops: `lj ops <command>

  status                                    summary and servers
  incidents [--status open|resolved]        list incidents
  resolve <id>                              resolve an incident
  cost [--analyse]                          cost recommendations (--analyse runs a new analysis)
  backup <target> [--wait]                  run a database backup now
  drill <target> [--wait]                   run a restore drill now`,
  models: `lj models <command>

  list                                      AI endpoints and private endpoints
  usage <id> [--private]                    usage of an endpoint
  finetune                                  fine-tuning jobs`,
  agents: `lj agents <command>

  list                                      hosted agents
  run <id> [--input json] [--wait]          start a run (--wait: poll; exit 1 if it did not succeed)
  runs <id>                                 recent runs`,
  comply: `lj comply <command>

  status                                    frameworks and automatic checks
  tasks [--status open|done]                compliance tasks
  recheck                                   re-run the automatic checks
  export [--out file]                       download the audit package (default audit-package.zip)`,
};

const VALUE_FLAGS = new Set(["key", "base-url", "org", "project", "issue", "title", "test-command", "repro-command", "severity", "fail-on", "status", "scan", "out",
  "data", "timeout", "interval", "reason", "input", "max-files"]);
const BOOL_FLAGS = new Set(["json", "wait", "no-wait", "help", "h", "analyse", "analyze", "private", "no-verify", "version"]);
class Usage extends Error {}

function parse(argv) {
  const args = [], flags = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "-h") { flags.help = true; continue; }
    if (!a.startsWith("--") || a === "--") { args.push(a); continue; }
    const eq = a.indexOf("=");
    const name = a.slice(2, eq < 0 ? undefined : eq);
    if (BOOL_FLAGS.has(name)) { flags[name] = eq < 0 ? true : a.slice(eq + 1) !== "false"; continue; }
    if (!VALUE_FLAGS.has(name)) throw new Usage(`unknown option --${name}`);
    const v = eq < 0 ? argv[++i] : a.slice(eq + 1);
    if (v === undefined) throw new Usage(`--${name} needs a value`);
    flags[name] = v;
  }
  return { args, flags };
}

// ------------------------------------------------------------------------------------------------ credentials
function credentialsPath(env = process.env) {
  const base = env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(base, "linearjeph", "credentials");
}
function readCredentials(env = process.env) {
  try {
    const text = fs.readFileSync(credentialsPath(env), "utf8").trim();
    if (text.startsWith("{")) { const j = JSON.parse(text); return { api_key: j.api_key, base_url: j.base_url }; }
    if (/^ljk_\S+$/.test(text)) return { api_key: text };
    const out = {};
    for (const line of text.split(/\r?\n/)) { const m = /^\s*([a-z_]+)\s*=\s*(.*?)\s*$/.exec(line); if (m) out[m[1]] = m[2]; }
    return out;
  } catch (_) { return {}; }
}
function writeCredentials(creds, env = process.env) {
  const file = credentialsPath(env);
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const clean = Object.fromEntries(Object.entries(creds).filter(([, v]) => v));
  fs.writeFileSync(file, JSON.stringify(clean) + "\n", { mode: 0o600 });
  fs.chmodSync(file, 0o600);   // writeFileSync keeps an existing file's mode
  return file;
}

// ------------------------------------------------------------------------------------------------ output
const cell = (v) => {
  if (v === null || v === undefined || v === "") return "-";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v).replace(/\s+/g, " ");
};
function table(rows, cols) {
  if (!rows.length) return "(none)";
  const data = rows.map((r) => cols.map(([, get]) => cell(typeof get === "function" ? get(r) : r[get])));
  const w = cols.map(([h], i) => Math.min(60, Math.max(h.length, ...data.map((d) => d[i].length))));
  const fit = (s, n) => (s.length > n ? s.slice(0, n - 1) + "…" : s.padEnd(n));
  const line = (cells) => cells.map((c, i) => fit(c, w[i])).join("  ").trimEnd();
  return [line(cols.map(([h]) => h)), ...data.map(line)].join("\n");
}
const kv = (pairs) => pairs.filter(([, v]) => v !== undefined && v !== null && v !== "").map(([k, v]) => `${k.padEnd(12)} ${cell(v)}`).join("\n");
const results = (r) => (Array.isArray(r) ? r : (r && r.results) || []);

const JOB_COLS = [["ID", "id"], ["STATUS", "status"], ["KIND", "kind"], ["PROJECT", "project"], ["TITLE", "title"], ["PR", "pr_url"]];
const FINDING_COLS = [["ID", "id"], ["SEVERITY", "severity"], ["CATEGORY", (f) => f.category || f.rule], ["TITLE", (f) => f.title || f.preview], ["WHERE", (f) => f.location || (f.file ? `${f.file}:${f.line}` : "")]];

// ------------------------------------------------------------------------------------------------ main
async function main(argv, env = process.env, io = {}) {
  const out = io.out || ((s) => process.stdout.write(s + "\n"));
  const err = io.err || ((s) => process.stderr.write(s + "\n"));
  let p;
  try { p = parse(argv); } catch (e) { err("lj: " + e.message + "\n" + "run \"lj --help\" for usage"); return 2; }
  const { args, flags } = p;
  const [cmd, sub] = args;
  if (flags.version) { out(SDK_VERSION); return 0; }
  if (!cmd || (flags.help && !HELP[cmd]) || cmd === "help") { (cmd && !flags.help && cmd !== "help" ? err : out)(HELP._); return cmd && !flags.help && cmd !== "help" ? 2 : 0; }
  if (flags.help) { out(HELP[cmd]); return 0; }

  const json = !!flags.json;
  const show = (data, text) => out(json ? JSON.stringify(data, null, 2) : text());
  const info = (s) => { if (!json) err(s); };

  try {
    if (cmd === "logout") {
      try { fs.unlinkSync(credentialsPath(env)); out("logged out"); } catch (e) { out("not logged in"); }
      return 0;
    }
    if (cmd === "login") return await login(flags, env, out, err);

    if (!["whoami", "fix", "shield", "ops", "models", "agents", "comply", "api"].includes(cmd)) { err(`lj: unknown command "${cmd}"\nrun "lj --help" for usage`); return 2; }
    const creds = readCredentials(env);
    const key = flags.key || env.LJ_API_KEY || creds.api_key;
    if (!key) { err("lj: no API key. Run \"lj login\", set LJ_API_KEY or pass --key."); return 2; }
    const c = new Client({ apiKey: key, baseUrl: flags["base-url"] || env.LJ_BASE_URL || creds.base_url, org: flags.org,
      fetch: io.fetch });
    const ctx = { c, flags, args: args.slice(2), show, out, err, info, json };

    if (cmd === "whoami") {
      const w = await c.whoami();
      show(w, () => kv([["organization", w.organization && `${w.organization.name} (#${w.organization.id})`], ["scopes", (w.scopes || []).join(", ")],
        ["key", w.key && `${w.key.name || ""} ${w.key.prefix || ""}`.trim()], ["expires", w.key && w.key.expires_at]]));
      return 0;
    }
    if (cmd === "api") return await apiCall(ctx, args.slice(1));
    if (!sub) { err(HELP[cmd]); return 2; }
    const handlers = { fix, shield, ops, models, agents, comply };
    const rc = await handlers[cmd](sub, ctx);
    if (rc === undefined) { err(`lj ${cmd}: unknown command "${sub}"\n\n${HELP[cmd]}`); return 2; }
    return rc;
  } catch (e) {
    if (e instanceof Usage) { err("lj: " + e.message); return 2; }
    if (e instanceof ApiError) {
      err(`lj: error: ${e.message} (${e.code}${e.status ? ", HTTP " + e.status : ""})`);
      return 2;
    }
    err("lj: " + (e && e.message));
    return 1;
  }
}

const need = (ctx, n, usage) => { if (ctx.args.length < n) throw new Usage("usage: " + usage); return ctx.args[0]; };
const flagNum = (flags, name, dflt) => { if (flags[name] === undefined) return dflt; const n = Number(flags[name]); if (!Number.isFinite(n) || n <= 0) throw new Usage(`--${name} must be a positive number`); return n; };
const pollOpts = (flags, onUpdate) => ({ timeout: flagNum(flags, "timeout", 900), interval: flagNum(flags, "interval", 3), onUpdate });
const needFlag = (flags, name, usage) => { if (flags[name] === undefined) throw new Usage("usage: " + usage); return flags[name]; };
const failOn = (flags, dflt) => {
  const v = flags["fail-on"] === undefined ? dflt : String(flags["fail-on"]).toLowerCase();
  if (v === undefined) return undefined;
  if (!SEVERITIES.slice(1).includes(v)) throw new Usage("--fail-on must be one of low, medium, high, critical");
  return v;
};

async function login(flags, env, out, err) {
  let key = flags.key || env.LJ_API_KEY;
  if (!key) {
    if (process.stdin.isTTY) err("API key (input is read from stdin and not stored in your shell history): ");
    key = await readStdinLine();
  }
  key = String(key || "").trim();
  if (!/^ljk_[a-z0-9]{8}_[\w-]{20,}$/.test(key)) { err("lj: that is not a LINEARJEPH API key (ljk_...). Create one in the dashboard under API keys."); return 2; }
  const baseUrl = flags["base-url"] || env.LJ_BASE_URL;
  let who = null;
  if (!flags["no-verify"]) {
    who = await new Client({ apiKey: key, baseUrl }).whoami();
  }
  const file = writeCredentials({ api_key: key, base_url: baseUrl }, env);
  out(`logged in${who && who.organization ? " to " + who.organization.name : ""}${who && who.scopes ? " (scopes: " + who.scopes.join(", ") + ")" : ""}\nsaved to ${file}`);
  return 0;
}
function readStdinLine() {
  return new Promise((resolve) => {
    let buf = "";
    process.stdin.setEncoding("utf8");
    const done = () => { process.stdin.pause(); resolve(buf.split(/\r?\n/)[0]); };
    process.stdin.on("data", (d) => { buf += d; if (/\n/.test(buf)) { process.stdin.removeAllListeners("data"); done(); } });
    process.stdin.on("end", done);
  });
}

async function apiCall(ctx, a) {
  const { c, flags } = ctx;
  if (a.length < 2) throw new Usage("usage: lj api <METHOD> <path> [--data json]");
  const method = a[0].toUpperCase();
  if (!/^(GET|POST|PUT|PATCH|DELETE)$/.test(method)) throw new Usage("METHOD must be GET, POST, PUT, PATCH or DELETE");
  let body;
  if (flags.data !== undefined) {
    try { body = JSON.parse(flags.data === "-" ? fs.readFileSync(0, "utf8") : flags.data); } catch (_) { throw new Usage("--data must be valid JSON (or - to read stdin)"); }
  }
  const r = await c.request(method, a[1], body);
  if (Buffer.isBuffer(r)) {
    if (!flags.out) throw new Usage("binary response: pass --out <file>");
    fs.writeFileSync(flags.out, r); ctx.out(`wrote ${r.length} bytes to ${flags.out}`);
  } else if (typeof r === "string") ctx.out(r);
  else ctx.out(JSON.stringify(r, null, 2));
  return 0;
}

// ------------------------------------------------------------------------------------------------ fix
async function fix(sub, ctx) {
  const { c, flags } = ctx;
  const showJob = (j) => ctx.show(j, () => kv([["job", j.id], ["status", `${j.status}${j.stage_note ? " (" + j.stage_note + ")" : ""}`], ["title", j.title], ["project", j.project],
    ["risk", j.risk], ["pull request", j.pr_url], ["error", j.error], ["root cause", j.root_cause], ["summary", j.summary]]));
  const waitJob = async (job) => {
    let last = "";
    const j = await c.fix.wait(job, pollOpts(flags, (x) => { if (x.status !== last) { last = x.status; ctx.info(`job ${x.id}: ${x.status}${x.stage_note ? " (" + x.stage_note + ")" : ""}`); } }));
    showJob(j);
    return FIX_BAD.includes(j.status) ? 1 : 0;
  };
  switch (sub) {
    case "jobs": {
      const r = await c.fix.jobs.list({ project: flags.project, status: flags.status });
      ctx.show(r, () => table(results(r), JOB_COLS)); return 0;
    }
    case "run": {
      const usage = 'lj fix run --project <id> --issue "<text>" [--wait]';
      const project = needFlag(flags, "project", usage), issue = needFlag(flags, "issue", usage);
      const j = await c.fix.run(project, { issue, title: flags.title, test_command: flags["test-command"], repro_command: flags["repro-command"] });
      if (flags.wait && !flags["no-wait"]) { ctx.info(`job ${j.id} started`); return waitJob(j.id); }
      showJob(j); return 0;
    }
    case "status": { showJob(await c.fix.jobs.get(need(ctx, 1, "lj fix status <job>"))); return 0; }
    case "wait": return waitJob(need(ctx, 1, "lj fix wait <job>"));
    case "accept": case "cancel": case "rollback": {
      const j = await c.fix.jobs[sub](need(ctx, 1, `lj fix ${sub} <job>`));
      ctx.show(j, () => (sub === "rollback" ? kv(Object.entries(j)) : kv([["job", j.id], ["status", j.status]]))); return 0;
    }
    case "reject": {
      const r = await c.fix.jobs.reject(need(ctx, 1, "lj fix reject <job> [--reason R]"), flags.reason);
      ctx.show(r, () => kv([["job", r.job && r.job.id], ["status", r.job && r.job.status], ["refunded", r.refunded]])); return 0;
    }
    case "review": case "deps": case "tests": {
      const proj = need(ctx, 1, `lj fix ${sub} <project>`);
      const r = await (sub === "tests" ? c.fix.projects.tests(proj, flags["max-files"] && Number(flags["max-files"])) : c.fix.projects[sub](proj));
      ctx.show(r, () => (r && r.id && r.status ? kv([["job", r.id], ["status", r.status], ["title", r.title]]) : kv(Object.entries(r || {})))); return 0;
    }
    default: return undefined;
  }
}

// ------------------------------------------------------------------------------------------------ shield
async function shield(sub, ctx) {
  const { c, flags } = ctx;
  const sevMin = (s) => (s === undefined ? 0 : SEVERITIES.indexOf(String(s).toLowerCase()));
  const sevFlag = (v) => { if (v !== undefined && sevMin(v) < 0) throw new Usage("--severity must be one of " + SEVERITIES.join(", ")); return v; };
  const summary = (s) => kv([["scan", s.id], ["status", s.status], ["grade", s.grade && `${s.grade} (${s.score})`], ["counts", s.counts], ["error", s.error]]);
  const findingsOut = (s, list) => table(list, FINDING_COLS);
  const opts = pollOpts(flags, (s) => ctx.info(`scan ${s.id}: ${s.status}`));
  switch (sub) {
    case "scan": {
      const project = needFlag(flags, "project", "lj shield scan --project <id> [--wait]");
      const q = await c.shield.vibe.scan(project);
      if (!flags.wait) { ctx.show(q, () => summary(q)); return 0; }
      const s = await c.shield.vibe.wait(q.id, opts);
      ctx.show(s, () => `${summary(s)}\n\n${findingsOut(s, s.findings || [])}`);
      return s.status === "failed" ? 1 : 0;
    }
    case "gate": {
      const usage = "lj shield gate --project <id> --fail-on <low|medium|high|critical>";
      const project = needFlag(flags, "project", usage); const level = failOn(flags, "high");
      const r = await c.shield.gateProject(project, { failOn: level, ...opts });
      ctx.show({ passed: r.passed, fail_on: level, scan: { id: r.scan.id, status: r.scan.status, counts: r.scan.counts }, findings: r.failing }, () => {
        const head = r.scan.status !== "done" ? `scan ${r.scan.id} ${r.scan.status}${r.scan.error ? ": " + r.scan.error : ""}` : summary(r.scan);
        return head + "\n\n" + (r.passed ? `PASS: no findings at or above ${level}` : r.scan.status !== "done" ? "FAIL: the scan did not finish" : `FAIL: ${r.failing.length} finding(s) at or above ${level}\n${findingsOut(r.scan, r.failing)}`);
      });
      return r.passed ? 0 : 1;
    }
    case "findings": {
      const min = sevFlag(flags.severity);
      if (!flags.project && !flags.scan) {
        const r = await c.shield.secrets.findings({ status: flags.status });
        const rows = gateFindings(results(r), min || "info");
        ctx.show(rows, () => table(rows, FINDING_COLS)); return 0;
      }
      let scan;
      if (flags.scan) scan = await c.shield.vibe.scanDetail(flags.scan);
      else {
        const p = ((await c.shield.vibe.projects()).projects || []).find((x) => String(x.id) === String(flags.project));
        if (!p) throw new ApiError(404, "not_found", "Project not found.");
        scan = p.last_scan ? await c.shield.vibe.scanDetail(p.last_scan.id) : { findings: [] };
      }
      const list = gateFindings(scan, min || "info");
      ctx.show(list, () => table(list, FINDING_COLS)); return 0;
    }
    case "secrets": {
      const r = await c.shield.secrets.overview();
      ctx.show(r, () => `${kv([["open", r.counts && r.counts.open], ["plan", r.plan]])}\n\n${table(r.sources || [], [["ID", "id"], ["KIND", "kind"], ["REF", "ref"], ["OPEN", "open"], ["LAST SCAN", "last_scan_at"]])}`); return 0;
    }
    case "pentest": {
      const target = need(ctx, 1, "lj shield pentest <target> [--wait] [--fail-on high]");
      const level = failOn(flags, undefined);
      const q = await c.shield.pentest.scan(target);
      if (!flags.wait) { ctx.show(q, () => kv([["scan", q.id], ["status", q.status]])); return 0; }
      const s = await c.shield.pentest.wait(q.id, opts);
      const failing = level && s.status === "done" ? gateFindings(s, level) : [];
      ctx.show(s, () => `${kv([["scan", s.id], ["host", s.host], ["status", s.status], ["counts", s.counts], ["error", s.error]])}\n\n${table(s.findings || [], FINDING_COLS)}`);
      return s.status === "failed" || failing.length ? 1 : 0;
    }
    case "waf": {
      const r = await c.shield.waf.overview();
      ctx.show(r, () => table(r.domains || [], [["ID", "id"], ["DOMAIN", "domain"], ["STATE", "state"], ["ENABLED", "enabled"]])); return 0;
    }
    default: return undefined;
  }
}

// ------------------------------------------------------------------------------------------------ ops
async function ops(sub, ctx) {
  const { c, flags } = ctx;
  const waitRun = async (target, kind, runId) => {
    const done = kind === "drills" ? ["passed", "failed"] : ["success", "failed"];
    const bad = "failed";
    const o = pollOpts(flags);
    const r = await c._poll(async () => ((await c.ops.backups.runs(target))[kind] || []).find((x) => x.id === runId) || { status: "running" }, (x) => done.includes(x.status), o);
    ctx.show(r, () => kv([[kind === "drills" ? "drill" : "backup", runId], ["status", r.status], ["detail", r.detail || r.error]]));
    return r.status === bad ? 1 : 0;
  };
  switch (sub) {
    case "status": {
      const [s, sv] = await Promise.all([c.ops.summary(), c.ops.servers()]);
      ctx.show({ summary: s, servers: sv }, () => `${kv([["servers", `${s.oncall.servers_used} of ${s.oncall.servers_limit}`], ["incidents", s.oncall.incidents_this_period]])}\n\n${table(results(sv), [["ID", "id"], ["NAME", "name"], ["STATUS", "status"], ["CPU%", "cpu_pct"], ["MEM%", "mem_pct"], ["DISK%", "disk_pct"], ["INCIDENTS", "open_incidents"]])}`);
      return 0;
    }
    case "incidents": {
      const r = await c.ops.incidents.list({ status: flags.status });
      ctx.show(r, () => table(results(r), [["ID", "id"], ["SEVERITY", "severity"], ["STATUS", "status"], ["SERVER", "server_name"], ["TITLE", "title"], ["OPENED", "opened_at"]])); return 0;
    }
    case "resolve": { const r = await c.ops.incidents.resolve(need(ctx, 1, "lj ops resolve <id>")); ctx.show(r, () => kv([["incident", r.id], ["status", r.status]])); return 0; }
    case "cost": {
      const r = flags.analyse || flags.analyze ? await c.ops.cost.analyse() : await c.ops.cost.summary();
      ctx.show(r, () => (r.recommendations ? table(r.recommendations, [["ID", "id"], ["TITLE", "title"], ["SAVING/MO", (x) => x.monthly_saving || x.saving], ["STATUS", "status"]]) : kv(Object.entries(r))));
      return 0;
    }
    case "backup": case "drill": {
      const t = need(ctx, 1, `lj ops ${sub} <target> [--wait]`);
      const r = await (sub === "backup" ? c.ops.backups.run(t) : c.ops.backups.drill(t));
      if (flags.wait) return waitRun(t, sub === "backup" ? "backups" : "drills", r.id);
      ctx.show(r, () => kv([[sub, r.id], ["status", r.status]])); return 0;
    }
    default: return undefined;
  }
}

// ------------------------------------------------------------------------------------------------ models
async function models(sub, ctx) {
  const { c, flags } = ctx;
  switch (sub) {
    case "list": {
      const [e, pr] = await Promise.all([c.models.endpoints.list(), c.models.private.list()]);
      ctx.show({ endpoints: e, private: pr }, () => `${table(results(e), [["ID", "id"], ["NAME", "name"], ["STATUS", "status"], ["COMPUTE", "compute_type"], ["URL", "invoke_url"]])}\n\nprivate endpoints\n${table(results(pr), [["ID", "id"], ["NAME", "name"], ["STATUS", "status"], ["MODEL", "model"]])}`);
      return 0;
    }
    case "usage": {
      const idv = need(ctx, 1, "lj models usage <id> [--private]");
      const r = await (flags.private ? c.models.private.usage(idv) : c.models.endpoints.usage(idv));
      ctx.show(r, () => (r.summary ? kv(Object.entries(r.summary)) + "\n\n" : "") + table(results(r).slice(0, 30), Object.keys(results(r)[0] || { value: 1 }).map((k) => [k.toUpperCase(), k])));
      return 0;
    }
    case "finetune": {
      const r = await c.models.finetune.jobs();
      ctx.show(r, () => table(results(r), [["ID", "id"], ["NAME", "name"], ["STATUS", "status"], ["MODEL", "base_model"], ["PROGRESS", "progress"], ["ERROR", "error"]])); return 0;
    }
    default: return undefined;
  }
}

// ------------------------------------------------------------------------------------------------ agents
async function agents(sub, ctx) {
  const { c, flags } = ctx;
  const runCols = [["RUN", "id"], ["STATUS", "status"], ["TRIGGER", "trigger"], ["SECONDS", "seconds"], ["ERROR", "error"]];
  switch (sub) {
    case "list": {
      const r = await c.agents.list();
      ctx.show(r, () => table(results(r), [["ID", "id"], ["NAME", "name"], ["STATUS", "status"], ["RUNTIME", "runtime"], ["SCHEDULE", "schedule"], ["LAST RUN", (a) => a.last_run && a.last_run.status]])); return 0;
    }
    case "run": {
      const agent = need(ctx, 1, "lj agents run <id> [--input json] [--wait]");
      let input; if (flags.input !== undefined) { try { input = JSON.parse(flags.input); } catch (_) { input = flags.input; } }
      const run = await c.agents.run(agent, input);
      if (!flags.wait) { ctx.show(run, () => table([run], runCols)); return 0; }
      const r = await c.agents.waitRun(agent, run.id, pollOpts(flags, (x) => ctx.info(`run ${x.id}: ${x.status}`)));
      ctx.show(r, () => kv([["run", r.id], ["status", r.status], ["error", r.error], ["output", r.output]]));
      return r.status === "succeeded" ? 0 : 1;
    }
    case "runs": {
      const r = await c.agents.runs(need(ctx, 1, "lj agents runs <id>"));
      ctx.show(r, () => table(results(r), runCols)); return 0;
    }
    default: return undefined;
  }
}

// ------------------------------------------------------------------------------------------------ comply
async function comply(sub, ctx) {
  const { c, flags } = ctx;
  switch (sub) {
    case "status": {
      const r = await c.comply.compliance();
      ctx.show(r, () => `${table(r.frameworks || [], Object.keys((r.frameworks || [{ name: 1 }])[0]).filter((k) => typeof (r.frameworks[0] || {})[k] !== "object").slice(0, 6).map((k) => [k.toUpperCase(), k]))}\n\nchecks\n${table(r.checks || [], [["CHECK", "label"], ["OK", "ok"], ["DETAIL", "detail"]])}`);
      return 0;
    }
    case "tasks": {
      const r = await c.comply.tasks.list();
      const rows = results(r).filter((t) => !flags.status || t.status === flags.status);
      ctx.show(rows, () => table(rows, [["ID", "id"], ["STATUS", "status"], ["TITLE", "title"], ["DUE", "due_date"], ["CONTROL", "control"]])); return 0;
    }
    case "recheck": { const r = await c.comply.recheck(); ctx.show(r, () => kv(Object.entries(r || {}).filter(([, v]) => typeof v !== "object"))); return 0; }
    case "export": {
      const buf = await c.comply.export();
      const file = flags.out || "audit-package.zip";
      fs.writeFileSync(file, buf);
      ctx.show({ file, bytes: buf.length }, () => `wrote ${buf.length} bytes to ${file}`); return 0;
    }
    default: return undefined;
  }
}

if (require.main === module) {
  main(process.argv.slice(2)).then((c) => { process.exitCode = c; }, (e) => { console.error("lj: " + (e && e.message)); process.exitCode = 1; });
}
module.exports = { main, parse, credentialsPath, readCredentials, writeCredentials, table };
