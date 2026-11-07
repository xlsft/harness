import { api } from "../util.ts";
import { webPath } from "./web.ts";

// HARNESS_VERSION: stamped into the image by the release workflow (deploy/Dockerfile). web: the path of the web UI ("" when
// none), which clients report as the place to open a session (packages/client, InitializeResponse.web_url).
export default api(() => ({ ok: true, version: process.env.HARNESS_VERSION || "0.0.0-dev", web: webPath() }));
