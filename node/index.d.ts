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
export function monitor<T>(name: string, fn: () => T | Promise<T>, opts?: { intervalS?: number; capture?: boolean; flush?: boolean }): Promise<T>;
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

declare const sdk: {
  SDK_VERSION: typeof SDK_VERSION; init: typeof init; flush: typeof flush; scrub: typeof scrub; setTag: typeof setTag;
  setUser: typeof setUser; captureException: typeof captureException; captureMessage: typeof captureMessage;
  captureTransaction: typeof captureTransaction; startTransaction: typeof startTransaction;
  withTransaction: typeof withTransaction; heartbeat: typeof heartbeat; monitor: typeof monitor;
  reportPackages: typeof reportPackages; packagesFromLock: typeof packagesFromLock; requestHandler: typeof requestHandler;
  errorHandler: typeof errorHandler; timing: typeof timing; expressErrorHandler: typeof expressErrorHandler;
  serializeError: typeof serializeError;
};
export default sdk;
