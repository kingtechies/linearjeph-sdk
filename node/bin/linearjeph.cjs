#!/usr/bin/env node
"use strict";
/**
 * linearjeph prophecy scan [dir] [--send] [--json] [--out file]
 * linearjeph <product> ... is the same as `lj <product> ...` (see lj.cjs).
 * linearjeph prophecy import <events|customers> <file.csv|file.json>
 * Keys: LINEARJEPH_INGEST_KEY (scan --send), LINEARJEPH_SECRET_KEY (import). Endpoint: LINEARJEPH_ENDPOINT.
 */
const fs = require("node:fs");
const path = require("node:path");
const prophecy = require("../prophecy.cjs");

const USAGE = `usage:
  linearjeph prophecy scan [dir] [--send] [--json] [--out file]
  linearjeph prophecy import <events|customers> <file.csv|file.json>
  linearjeph <fix|shield|ops|models|agents|comply|api|login|logout|whoami> ...   same as the lj command (lj --help)
env: LINEARJEPH_INGEST_KEY (scan --send), LINEARJEPH_SECRET_KEY (import), LINEARJEPH_ENDPOINT (default https://linearjeph.cc)`;

/** RFC 4180 CSV -> array of objects keyed by the header row. */
function parseCsv(text) {
  const rows = []; let row = [], cell = "", q = false;
  text = text.replace(/^﻿/, "");
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (q) { if (c === '"') { if (text[i + 1] === '"') { cell += '"'; i++; } else q = false; } else cell += c; }
    else if (c === '"') q = true;
    else if (c === ",") { row.push(cell); cell = ""; }
    else if (c === "\n" || c === "\r") { if (c === "\r" && text[i + 1] === "\n") i++; row.push(cell); cell = ""; if (row.some((x) => x !== "")) rows.push(row); row = []; }
    else cell += c;
  }
  row.push(cell); if (row.some((x) => x !== "")) rows.push(row);
  const head = (rows.shift() || []).map((h) => h.trim());
  return rows.map((r) => { const o = {}; head.forEach((h, i) => { if (h && r[i] !== undefined && r[i] !== "") o[h] = r[i]; }); return o; });
}
function loadRows(file, kind) {
  const text = fs.readFileSync(file, "utf8");
  if (/\.csv$/i.test(file)) return parseCsv(text);
  const j = JSON.parse(text);
  return Array.isArray(j) ? j : j[kind] || j.rows || [];
}

const LJ_COMMANDS = new Set(["fix", "shield", "ops", "models", "agents", "comply", "api", "login", "logout", "whoami"]);

async function main(argv) {
  if (argv.length && !argv[0].startsWith("-") && LJ_COMMANDS.has(argv[0])) return require("./lj.cjs").main(argv);
  const args = argv.filter((a) => !a.startsWith("--")), flag = (n) => argv.includes("--" + n);
  if (args[0] !== "prophecy" || !["scan", "import"].includes(args[1])) { console.error(USAGE); return 2; }
  prophecy.init({ debug: false, handlers: false });
  if (args[1] === "scan") {
    const dir = args[2] || ".";
    const t0 = Date.now();
    const m = prophecy.scan(dir);
    const oi = argv.indexOf("--out");
    if (oi >= 0 && argv[oi + 1]) fs.writeFileSync(argv[oi + 1], JSON.stringify(m, null, 2));
    if (flag("json")) console.log(JSON.stringify(m, null, 2));
    console.error(`scanned ${path.resolve(dir)} in ${Date.now() - t0} ms: ${m.loc} lines, ${m.routes.length} routes, ${m.dependencies.length} dependencies, ` +
      `${m.features.length} features, ${m.integrations.length} integrations, ${m.security.secrets_suspected} suspected secrets (counted, never sent)`);
    if (flag("send")) {
      const ok = await prophecy.sendManifest(m);
      console.error(ok ? "manifest sent" : "manifest NOT sent (check LINEARJEPH_INGEST_KEY and the endpoint)");
      return ok ? 0 : 1;
    }
    return 0;
  }
  const kind = args[2], file = args[3];
  if (!["events", "customers"].includes(kind) || !file) { console.error(USAGE); return 2; }
  const rows = loadRows(file, kind);
  const r = await prophecy.importHistory(kind, rows, { onProgress: (p) => console.error(`imported ${p.done}/${p.total} (accepted ${p.accepted}, rejected ${p.rejected})`) });
  console.error(`done: ${r.accepted} accepted, ${r.rejected} rejected`);
  for (const e of r.errors) console.error("  " + JSON.stringify(e));
  return r.rejected && !r.accepted ? 1 : 0;
}
if (require.main === module) {
  main(process.argv.slice(2)).then((c) => { process.exitCode = c; }, (e) => { console.error("error: " + (e && e.message)); process.exitCode = 1; });
}
module.exports = { parseCsv, main };
