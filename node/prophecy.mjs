import { createRequire } from "node:module";
const p = createRequire(import.meta.url)("./prophecy.cjs");
export const {
  SDK_VERSION, init, track, feature, signup, payment, subscription, churn, identify, middleware, nextMiddleware, scan, sendManifest,
  importHistory, report, refresh, live, embedUrl, flush, close, normalizePath,
} = p;
export default p;
