/**
 * GET /api/vcs/github/manifest/callback?code=...&state=... — redirect target of the GitHub App manifest
 * flow (VcsService.GetGitHubAppManifest). The state (issued to an admin) authorizes the exchange; the same
 * code can also be completed with CompleteGitHubAppManifest.
 */
import { defineHandler } from "h3";
import { ApiError } from "../../lib/errors.ts";
import { installUrl } from "../../vcs/connections.ts";
import { completeGitHubAppManifest } from "../../vcs/manifest.ts";
import { resultPage } from "../result-page.ts";

export default defineHandler(async (event) => {
  const q = event.url.searchParams;
  try {
    const row = await completeGitHubAppManifest(q.get("code") ?? "", q.get("state") ?? "");
    const next = installUrl(row);
    if (next) return new Response(null, { status: 303, headers: { location: next, "cache-control": "no-store", "referrer-policy": "no-referrer" } });
    return resultPage(true, "GitHub App registered", `The app ${row.displayName} is connected. Install it on your repositories next.`, 200);
  } catch (err) {
    const msg = err instanceof ApiError ? err.message : "Something went wrong while registering the app. Try again.";
    if (!(err instanceof ApiError)) console.error("[harness] manifest callback failed", err);
    return resultPage(false, "Registration failed", msg, err instanceof ApiError ? err.status : 500);
  }
});
