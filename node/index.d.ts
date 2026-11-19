export type Status = "ok" | "error";

export interface Event {
  type: "error" | "transaction";
  timestamp: string;
  release: string;
  environment: string;
  exception?: { type: string; value: string; stacktrace: { filename: string; function: string; lineno: number; in_app: boolean }[] };
  transaction?: { name: string; duration_ms: number; status: string };
  tags: Record<string, string>;
  user: { id?: string };
}

export interface InitOptions {
  /** Application ingest key (Dashboard > Maintenance > your app). Required. */
  key: string;
  /** Default "https://linearjeph.cc". */
  endpoint?: string;
  release?: string;
  /** Default "production". */
  environment?: string;
  /** Fraction of errors/messages sent, 0..1. Default 1. */
  sampleRate?: number;
  /** Fraction of transactions sent, 0..1. Default 1. */
  tracesSampleRate?: number;
  /** Modify an event or return null/undefined to drop it. A throwing hook drops the event. */
  beforeSend?: (event: Event) => Event | null | undefined;
  /** Extra tag-key substrings to filter (case-insensitive), on top of the built-in list. */
  scrubKeys?: string[];
  /** Max queued items before new ones are dropped. Default 100. */
  maxQueue?: number;
  /** Max events per request (1..100). Default 20. */
  batchSize?: number;
  /** Retries after the first attempt for 429/5xx/network errors. Default 4. */
  retries?: number;
  /** Base backoff in ms (doubles per attempt, with jitter). Default 500. */
  backoffMs?: number;
  /** Per-request timeout in ms. Default 10000. */
  timeoutMs?: number;
  /** Install uncaught-exception capture and flush on beforeExit. Default true. */
  handlers?: boolean;
  /** With handlers: after capturing an uncaught exception, flush (max 2s) and exit(1) like Node does. Default true. */
  exitOnUncaught?: boolean;
  /** Max wait for the exit flush in ms. Default 5000. */
  flushTimeoutMs?: number;
  /** Log SDK problems to stderr. Default false. */
  debug?: boolean;
  /** Metric flush interval in ms (min 1000). Default 10000. */
  metricsIntervalMs?: number;
  /** Max distinct metric series kept in memory per interval. Default 500. */
  maxMetricSeries?: number;
  /** Scrub emails, card numbers and tokens from logs, tags and metric tags before sending. Default true. */
  piiScrub?: boolean;
  /** Minimum log level sent: "debug" | "info" | "warning" | "error" | "critical". Default "info". */
  logLevel?: string;
  /** Fraction of sub-error log lines sent, 0..1. Default 1. */
  sampleLogs?: number;
  /** Modify a log item or return null to drop it. */
  beforeSendLog?: (item: LogItem) => LogItem | null | undefined;
}

export interface LogItem {
  timestamp: string; level: string; message: string; logger: string; context: Record<string, unknown>;
  release: string; environment: string; trace_id?: string; span_id?: string;
}
export type LogLevel = "debug" | "info" | "warning" | "error" | "critical";
export type Tags = Record<string, string | number | boolean>;

export interface Span {
  name: string; kind: string; traceId: string; spanId: string; parentId: string; sampled: boolean;
  setTag(key: string, value: unknown): Span;
  traceparent(): string;
  end(status?: Status, name?: string, tags?: Tags): void;
}
export interface SpanOptions { kind?: "internal" | "server" | "client" | "producer" | "consumer"; tags?: Tags; traceparent?: string }

export interface CheckinOptions {
  runId?: string; durationMs?: number; message?: string; schedule?: string; tz?: string; graceS?: number; intervalS?: number; maxRuntimeS?: number;
}
export interface MonitorOptions { intervalS?: number; schedule?: string; tz?: string; graceS?: number; maxRuntimeS?: number; capture?: boolean; flush?: boolean }

export type HealthResult = boolean | number | { status?: "pass" | "warn" | "fail"; value?: number; message?: string };
export interface HealthBody { status: "ok" | "degraded" | "fail"; checks: Record<string, { status: "pass" | "warn" | "fail"; value?: number; message?: string }> }
export interface HealthApi {
  register(name: string, fn: () => HealthResult | Promise<HealthResult>, opts?: { warn?: number; fail?: number; below?: boolean; timeoutMs?: number }): void;
  unregister(name: string): void;
  run(): Promise<HealthBody>;
  respond(authorization?: string | null, token?: string): Promise<{ status: number; body: HealthBody | { status: string } }>;
  handler(opts?: { token?: string }): (req: any, res: any) => void;
  routeHandler(opts?: { token?: string }): (request: Request) => Promise<Response>;
}

export interface CaptureContext { tags?: Record<string, string> }

export const SDK_VERSION: string;
export function init(opts: InitOptions | string): boolean;
export function flush(timeoutMs?: number): Promise<boolean>;
export function scrub<T = unknown>(data: T): T;
export function setTag(key: string, value: string): void;
export function setUser(id: string | number | null): void;
export function captureException(err: unknown, ctx?: CaptureContext): void;
export function captureMessage(msg: string, level?: string, ctx?: CaptureContext): void;
export function captureTransaction(name: string, durationMs: number, status?: string, ctx?: CaptureContext): void;
export function startTransaction(name: string): { end(status?: string): void };
export function withTransaction<T>(name: string, fn: () => T | Promise<T>): Promise<T>;
export function heartbeat(name: string, status?: Status, intervalS?: number): void;
export function monitor<T>(name: string, fn: () => T | Promise<T>, opts?: MonitorOptions): Promise<T>;
export function checkin(name: string, status?: "start" | "ok" | "fail", opts?: CheckinOptions): void;

export function incr(name: string, value?: number, tags?: Tags): void;
export function gauge(name: string, value: number, tags?: Tags): void;
export function histogram(name: string, value: number, tags?: Tags): void;
export function time<T>(name: string, fn: () => T | Promise<T>, tags?: Tags): Promise<T>;
export function startTimer(name: string, tags?: Tags): { end(): void };
export function flushMetrics(): number;
export function droppedMetricSeries(): number;

export function log(level: LogLevel | number, message: string, context?: Record<string, unknown>, logger?: string): void;
export const logs: Record<LogLevel, (message: string, context?: Record<string, unknown>, logger?: string) => void>;
export function captureConsole(levels?: ("log" | "info" | "warn" | "error" | "debug")[]): () => void;
export function pinoStream(): { write(chunk: string | Uint8Array): void };
export function winstonTransport(): new (opts?: unknown) => { log(info: any, cb?: () => void): void };
export function scrubPii(text: string): string;

export function span<T>(name: string, fn: (span: Span) => T | Promise<T>, opts?: SpanOptions): Promise<T>;
export function startSpan(name: string, opts?: SpanOptions): Span;
export function currentSpan(): Span | null;
export function injectHeaders<H extends Record<string, string>>(headers?: H): H & { traceparent?: string };
export function parseTraceparent(v: string): { traceId: string; parentId: string; sampled: boolean } | null;
export function wrapNext<F extends (...args: any[]) => any>(handler: F, opts?: { name?: string }): F;

export function release(opts?: { version?: string; commit?: string; notes?: string }): void;
export function deploy(opts?: { version?: string; environment?: string; status?: "started" | "success" | "failed"; url?: string; commit?: string; notify?: boolean }): void;
export const health: HealthApi;
export function reportPackages(lockPath?: string): number;
export function packagesFromLock(lock: unknown): { name: string; version: string }[];

type Next = (e?: unknown) => void;
export function requestHandler(opts?: { ignorePaths?: string[] }): (req: any, res: any, next: Next) => void;
export function errorHandler(): (err: unknown, req: any, res: any, next: Next) => void;
/** Alias of requestHandler (kept from 0.x). */
export const timing: typeof requestHandler;
/** Alias of errorHandler. */
export const expressErrorHandler: typeof errorHandler;
export function serializeError(err: unknown): Event;
/** Prophecy growth intelligence: see ./prophecy.d.ts. Also importable as "@linearjeph/sdk/prophecy". */
import * as prophecyApi from "./prophecy";
export const prophecy: typeof prophecyApi;
export type { Manifest as ProphecyManifest, ProphecyInitOptions } from "./prophecy";

declare const sdk: {
  SDK_VERSION: typeof SDK_VERSION; init: typeof init; flush: typeof flush; scrub: typeof scrub; setTag: typeof setTag;
  setUser: typeof setUser; captureException: typeof captureException; captureMessage: typeof captureMessage;
  captureTransaction: typeof captureTransaction; startTransaction: typeof startTransaction;
  withTransaction: typeof withTransaction; heartbeat: typeof heartbeat; monitor: typeof monitor;
  reportPackages: typeof reportPackages; packagesFromLock: typeof packagesFromLock; requestHandler: typeof requestHandler;
  errorHandler: typeof errorHandler; timing: typeof timing; expressErrorHandler: typeof expressErrorHandler;
  serializeError: typeof serializeError; checkin: typeof checkin; incr: typeof incr; gauge: typeof gauge; histogram: typeof histogram;
  time: typeof time; startTimer: typeof startTimer; log: typeof log; logs: typeof logs; span: typeof span; startSpan: typeof startSpan;
  currentSpan: typeof currentSpan; injectHeaders: typeof injectHeaders; wrapNext: typeof wrapNext; release: typeof release;
  deploy: typeof deploy; health: HealthApi; scrubPii: typeof scrubPii; pinoStream: typeof pinoStream; winstonTransport: typeof winstonTransport;
  captureConsole: typeof captureConsole; prophecy: typeof prophecyApi;
};
export default sdk;
