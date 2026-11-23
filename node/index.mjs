import { createRequire } from "node:module";
const lj = createRequire(import.meta.url)("./index.cjs");
export const {
  SDK_VERSION, init, flush, scrub, setTag, setUser,
  captureException, captureMessage, captureTransaction, startTransaction, withTransaction,
  heartbeat, monitor, reportPackages, packagesFromLock,
  incr, gauge, histogram, time, startTimer, span, startSpan, currentSpan, injectHeaders, parseTraceparent, log, logs, captureConsole,
  pinoStream, winstonTransport, checkin, release, deploy, health, wrapNext, scrubPii, flushMetrics, droppedMetricSeries,
  Client, ApiError, requestHandler, errorHandler, timing, expressErrorHandler, serializeError, prophecy,
} = lj;
export default lj;
