import { createRequire } from "node:module";
const lj = createRequire(import.meta.url)("./index.cjs");
export const {
  SDK_VERSION, init, flush, scrub, setTag, setUser,
  captureException, captureMessage, captureTransaction, startTransaction, withTransaction,
  heartbeat, monitor, reportPackages, packagesFromLock,
  requestHandler, errorHandler, timing, expressErrorHandler, serializeError,
} = lj;
export default lj;
