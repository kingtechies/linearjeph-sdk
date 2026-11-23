export const SDK_VERSION: string;
export const DEFAULT_BASE_URL: string;
export type Severity = "info" | "low" | "medium" | "high" | "critical";
export const SEVERITIES: Severity[];
export const ROUTES: Record<string, string>;

export class ApiError extends Error {
  constructor(status: number, code: string, message: string, body?: unknown);
  /** HTTP status; 0 for network errors, timeouts and usage errors. */
  status: number;
  /** Platform error code, e.g. "not_subscribed", "not_found", "timeout", "network". */
  code: string;
  body: unknown;
}

export interface ClientOptions {
  /** Default: LJ_API_KEY. */
  apiKey?: string;
  /** Default: LJ_BASE_URL, then https://linearjeph.cc. */
  baseUrl?: string;
  /** Organization id, sent as ?org=. */
  org?: string | number;
  /** Seconds per request. Default 30. */
  timeout?: number;
  /** Total tries for GET on 429/5xx/network errors. Default 3. */
  retries?: number;
  backoffMs?: number;
  fetch?: typeof fetch;
}

export interface WaitOptions<T = any> {
  /** Seconds. Default 600. */
  timeout?: number;
  /** Seconds between polls. Default 3. */
  interval?: number;
  onUpdate?: (current: T) => void;
}

export type Json = any;
export interface Finding { id?: number; severity: Severity | string; title?: string; category?: string; [k: string]: any }
export interface ScanDetail { id: number; status: "queued" | "running" | "done" | "failed" | string; counts?: Record<string, number>; findings?: Finding[]; error?: string; [k: string]: any }
export interface FixJob { id: number; status: string; title: string; project: number | null; pr_url: string; error: string; [k: string]: any }

export interface FixApi {
  overview(): Promise<Json>;
  jobs: {
    list(query?: { project?: number | string; kind?: string; status?: string; product?: string }): Promise<{ results: FixJob[] }>;
    create(body: { source_id: string; title: string; issue?: string; logs?: string; repro_command?: string; test_command?: string; url?: string; run_on?: string; server_ref?: string }): Promise<FixJob>;
    get(job: number | string): Promise<FixJob>;
    events(job: number | string, after?: number): Promise<Json>;
    cancel(job: number | string): Promise<FixJob>;
    accept(job: number | string): Promise<FixJob>;
    reject(job: number | string, reason?: string): Promise<Json>;
    rollback(job: number | string): Promise<Json>;
    /** Unified diff text. */
    patch(job: number | string): Promise<string>;
  };
  projects: {
    list(): Promise<{ results: Json[] }>;
    get(project: number | string): Promise<Json>;
    update(project: number | string, body: Json): Promise<Json>;
    review(project: number | string): Promise<Json>;
    deps(project: number | string): Promise<FixJob>;
    tests(project: number | string, maxFiles?: number): Promise<FixJob>;
  };
  migrations: { list(): Promise<{ results: Json[] }>; get(id: number | string): Promise<Json>; plan(id: number | string): Promise<Json> };
  /** Poll until done, accepted, failed, rejected, cancelled, or pr_open (waiting for a human). */
  wait(job: number | string, opts?: WaitOptions<FixJob>): Promise<FixJob>;
  /** `project` is a Fix project id (digits) or a source id. */
  run(project: number | string, opts: { issue?: string; title?: string; logs?: string; repro_command?: string; test_command?: string; url?: string }): Promise<FixJob>;
}

export interface ShieldApi {
  overview(): Promise<Json>;
  vibe: { projects(): Promise<{ projects: Json[] }>; scan(project: number | string): Promise<ScanDetail>; scanDetail(scan: number | string): Promise<ScanDetail>; wait(scan: number | string, opts?: WaitOptions<ScanDetail>): Promise<ScanDetail> };
  secrets: { overview(): Promise<Json>; sources(): Promise<Json[]>; scan(source: number | string): Promise<Json>; findings(query?: { status?: string }): Promise<{ results: Finding[] }> };
  pentest: { overview(): Promise<Json>; scan(target: number | string): Promise<Json>; scanDetail(scan: number | string): Promise<ScanDetail>; history(target: number | string): Promise<Json>; retest(finding: number | string): Promise<Json>; wait(scan: number | string, opts?: WaitOptions<ScanDetail>): Promise<ScanDetail> };
  waf: { overview(): Promise<Json>; events(domain: number | string): Promise<Json> };
  phishing: { overview(): Promise<Json>; campaigns(): Promise<Json[]>; campaign(id: number | string): Promise<Json> };
  security: { overview(): Promise<Json>; scans(): Promise<Json[]>; scan(scan: number | string): Promise<ScanDetail>; start(target: number | string): Promise<ScanDetail>; wait(scan: number | string, opts?: WaitOptions<ScanDetail>): Promise<ScanDetail> };
  /** Findings at or above `failOn` (low, medium, high, critical). */
  gate(scan: ScanDetail | Finding[], failOn?: Exclude<Severity, "info">): Finding[];
  gateProject(project: number | string, opts?: WaitOptions<ScanDetail> & { failOn?: Exclude<Severity, "info">; wait?: boolean }): Promise<{ scan: ScanDetail; failing: Finding[]; passed: boolean }>;
}

export interface OpsApi {
  summary(): Promise<Json>;
  servers(): Promise<{ results: Json[] }>;
  incidents: { list(query?: { status?: "open" | "resolved" }): Promise<{ results: Json[] }>; get(id: number | string): Promise<Json>; resolve(id: number | string): Promise<Json> };
  cost: { summary(): Promise<Json>; analyse(): Promise<Json> };
  managedDatabases: { list(): Promise<{ results: Json[] }> };
  backups: { targets(): Promise<{ results: Json[] }>; run(target: number | string): Promise<Json>; drill(target: number | string): Promise<Json>; runs(target: number | string): Promise<{ backups: Json[]; drills: Json[] }> };
}

export interface ModelsApi {
  endpoints: { list(): Promise<{ results: Json[] }>; get(id: number | string): Promise<Json>; usage(id: number | string): Promise<Json>; action(id: number | string, action: "suspend" | "resume" | "rotate-key"): Promise<Json> };
  private: { list(): Promise<{ results: Json[] }>; get(id: number | string): Promise<Json>; usage(id: number | string): Promise<Json>; logs(id: number | string): Promise<Json> };
  finetune: { datasets(): Promise<{ results: Json[] }>; jobs(): Promise<{ results: Json[] }>; job(id: number | string): Promise<Json>; createJob(body: Json): Promise<Json>; jobAction(id: number | string, action: "cancel" | "deploy", body?: Json): Promise<Json> };
}

export interface AgentsApi {
  overview(): Promise<Json>;
  list(): Promise<{ results: Json[] }>;
  get(id: number | string): Promise<Json>;
  runs(id: number | string): Promise<{ results: Json[] }>;
  runDetail(id: number | string, run: number | string): Promise<Json>;
  cancelRun(id: number | string, run: number | string): Promise<Json>;
  action(id: number | string, action: "run" | "pause" | "resume" | "redeploy" | "rotate-webhook", body?: Json): Promise<Json>;
  run(id: number | string, input?: Json): Promise<Json>;
  waitRun(id: number | string, run: number | string, opts?: WaitOptions): Promise<Json>;
}

export interface ComplyApi {
  overview(): Promise<Json>;
  compliance(): Promise<{ frameworks: Json[]; checks: Json[] }>;
  frameworks(): Promise<Json[]>;
  framework(key: string): Promise<Json>;
  tasks: { list(): Promise<{ results: Json[] }>; create(body: { title: string; control?: number; due_date?: string; owner?: number }): Promise<Json>; update(id: number | string, body: Json): Promise<Json> };
  recheck(): Promise<Json>;
  /** The audit package (zip). */
  export(): Promise<Buffer>;
}

export class Client {
  constructor(opts?: ClientOptions | string);
  baseUrl: string;
  org: string | number | null;
  fix: FixApi; shield: ShieldApi; ops: OpsApi; models: ModelsApi; agents: AgentsApi; comply: ComplyApi;
  /** Any route. `path` is relative to /api/v1/ or absolute. Resolves to JSON, text (string), a Buffer or null. */
  request(method: string, path: string, body?: Json, query?: Record<string, string | number | undefined>): Promise<any>;
  whoami(): Promise<{ organization: { id: number; name: string }; scopes: string[]; key: { prefix: string; name: string; expires_at: string | null } }>;
}

export function gateFindings(scan: ScanDetail | Finding[], failOn?: Exclude<Severity, "info">): Finding[];
