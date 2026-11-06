import { api } from "../util.ts";

// HARNESS_VERSION: stamped into the image by the release workflow (deploy/Dockerfile).
export default api(() => ({ ok: true, version: process.env.HARNESS_VERSION || "0.0.0-dev" }));
