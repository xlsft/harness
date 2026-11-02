/**
 * GET /api/vcs/oauth/callback?code=...&state=... — redirect target of per-user code host authorization
 * (VcsService.StartUserAuth; GitHub App user-to-server OAuth, GitLab OAuth application). The flow is found
 * by `state` alone; the client polls GetUserAuthStatus with its flow id.
 */
import { defineHandler } from "h3";
import { handleUserAuthCallback } from "../../vcs/auth.ts";
import { resultPage } from "../result-page.ts";

export default defineHandler(async (event) => {
  try {
    const r = await handleUserAuthCallback(event.url.searchParams);
    const who = r.provider ? `your ${r.provider} account` : "your account";
    return r.ok
      ? resultPage(true, "Connected", `harness can now act as ${who} on all your devices. You can close this window.`, 200)
      : resultPage(false, "Authorization failed", r.message, 400);
  } catch (err) {
    console.error("[harness] vcs oauth callback failed", err);
    return resultPage(false, "Authorization failed", "Something went wrong while finishing the authorization. Try again.", 500);
  }
});
