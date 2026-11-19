/** LINEARJEPH Prophecy: growth-intelligence tracking, codebase scan, history import and report access. */
export const SDK_VERSION: string;

export type ProphecyKind = "signup" | "login" | "active" | "feature" | "payment" | "refund" | "subscription" | "churn" | "pageview" | "request" | "error" | "custom";

export interface ProphecyInitOptions {
  /** Application ingest key (write-only). Falls back to env LINEARJEPH_INGEST_KEY. Keys starting with ljp_sk_ are refused. */
  ingestKey?: string;
  /** Project secret key (ljp_sk_...), server side only. Falls back to env LINEARJEPH_SECRET_KEY. Ignored in a browser. */
  secretKey?: string;
  /** Default "https://linearjeph.cc" (or env LINEARJEPH_ENDPOINT). */
  endpoint?: string;
  /** Replace user ids with their SHA-256 hex before sending. Default true. */
  hashUserIds?: boolean;
  /** Events per request (1..500). Default 100. */
  batchSize?: number;
  /** Flush interval in ms. Default 2000. */
  flushIntervalMs?: number;
  /** Retries after the first attempt for 429/5xx/network errors. Default 4. */
  retries?: number;
  /** Base backoff in ms. Default 500. */
  backoffMs?: number;
  /** Per-request timeout in ms. Default 10000. */
  timeoutMs?: number;
  /** Flush on beforeExit. Default true. */
  handlers?: boolean;
  debug?: boolean;
}

export interface TrackOptions {
  uid?: string | number;
  kind?: ProphecyKind;
  feature?: string;
  amount?: number;
  currency?: string;
  /** Max 2 KB serialised; larger props are dropped. */
  props?: Record<string, unknown>;
  /** Date, ISO string or epoch ms. Default now. */
  ts?: Date | string | number;
}

export interface SignupOptions { plan?: string; channel?: string; country?: string; ts?: Date | string | number }
export interface PaymentOptions { plan?: string; currency?: string; ts?: Date | string | number }
export interface SubscriptionOptions { plan: string; mrr: number; status?: "active" | "cancelled" | "paused" }
export interface Traits { signup_at?: string; plan?: string; mrr?: number; country?: string; channel?: string; [trait: string]: unknown }

export interface MiddlewareOptions {
  /** Path prefixes not tracked, e.g. ["/health"]. */
  ignorePaths?: string[];
  /** Resolve the signed-in user id (hashed like every uid). */
  uid?: (req: any) => string | number | undefined;
}
export type Next = (err?: unknown) => void;

export interface Manifest {
  schema: 1;
  scanned_at: string;
  root_name: string;
  languages: Record<string, number>;
  loc: number;
  frameworks: string[];
  dependencies: { ecosystem: "npm" | "pypi" | "go" | "composer" | "gem" | "maven"; name: string; version: string }[];
  routes: { method: string; path: string; file: string }[];
  features: { name: string; source: "route" | "flag" | "dir" | "config"; paths: string[] }[];
  integrations: string[];
  infra: { docker: boolean; compose: boolean; kubernetes: boolean; terraform: boolean; serverless: boolean; ci: string[]; hosting_hints: string[] };
  env_names: string[];
  pricing_hints: { plan: string; amount: number; currency: string }[];
  description: string;
  tests: { files: number; frameworks: string[] };
  security: { lockfile: boolean; env_committed: boolean; secrets_suspected: number };
}

export interface EventRow { ts: string | number; uid?: string; name: string; kind?: ProphecyKind; feature?: string; amount?: number | string; currency?: string; props?: Record<string, unknown> | string; id?: string }
export interface CustomerRow { uid: string; signup_at?: string; plan?: string; mrr?: number | string; country?: string; channel?: string; traits?: Record<string, unknown>; id?: string }
export interface ImportProgress { done: number; total: number; accepted: number; rejected: number }
export interface ImportResult { accepted: number; rejected: number; errors: unknown[]; jobs: unknown[] }

export function init(options: ProphecyInitOptions): boolean;
export function track(name: string, options?: TrackOptions): void;
export function feature(featureName: string, uid?: string | number): void;
export function signup(uid: string | number, options?: SignupOptions): void;
export function payment(uid: string | number, amount: number, currency?: string, options?: PaymentOptions): void;
export function payment(uid: string | number, amount: number, options?: PaymentOptions): void;
export function subscription(uid: string | number, options: SubscriptionOptions): void;
export function churn(uid: string | number, reason?: string): void;
export function identify(uid: string | number, traits?: Traits): void;
/** Express/Connect middleware: records route template, method, status and duration. Never bodies, headers or query strings. */
export function middleware(options?: MiddlewareOptions): (req: any, res: any, next: Next) => void;
/** Wrap a Next.js route handler (app router). Pass `route` for an exact template. */
export function nextMiddleware<F extends (req: any, ctx?: any) => any>(handler: F, options?: { route?: string }): F;
export function scan(rootDir?: string, options?: { maxFiles?: number }): Manifest;
export function sendManifest(manifest: Manifest): Promise<boolean>;
export function importHistory(kind: "events", rows: EventRow[], options?: { onProgress?: (p: ImportProgress) => void }): Promise<ImportResult>;
export function importHistory(kind: "customers", rows: CustomerRow[], options?: { onProgress?: (p: ImportProgress) => void }): Promise<ImportResult>;
export function report(section?: string): Promise<{ report: any; created_at: string; stale: boolean }>;
export function refresh(): Promise<{ queued: boolean }>;
export function live(): Promise<any>;
/** Signed URL for an iframe in your own admin panel (ttl seconds, 60..86400, default 3600). */
export function embedUrl(ttlSeconds?: number): Promise<string>;
export function flush(timeoutMs?: number): Promise<boolean>;
export function close(timeoutMs?: number): Promise<boolean>;
export function normalizePath(path: string): string;

declare const prophecy: {
  SDK_VERSION: string; init: typeof init; track: typeof track; feature: typeof feature; signup: typeof signup; payment: typeof payment;
  subscription: typeof subscription; churn: typeof churn; identify: typeof identify; middleware: typeof middleware;
  nextMiddleware: typeof nextMiddleware; scan: typeof scan; sendManifest: typeof sendManifest; importHistory: typeof importHistory;
  report: typeof report; refresh: typeof refresh; live: typeof live; embedUrl: typeof embedUrl; flush: typeof flush; close: typeof close;
  normalizePath: typeof normalizePath;
};
export default prophecy;
