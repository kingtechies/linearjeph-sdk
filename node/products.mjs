import { createRequire } from "node:module";
const p = createRequire(import.meta.url)("./products.cjs");
export const { Client, ApiError, SDK_VERSION, DEFAULT_BASE_URL, SEVERITIES, gateFindings, ROUTES } = p;
export default p;
