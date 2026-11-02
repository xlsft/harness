/**
 * GET /api/mcp/oauth/callback?code=...&state=... — redirect target of MCP OAuth flows (McpService.StartOAuth).
 * The flow is identified by `state` alone (the browser may not be signed in to harness, e.g. a URL opened
 * from the terminal). Finishes the flow, then redirects to the flow's return_url or shows a result page.
 */
import { defineHandler } from "h3";
import { handleCallback } from "../../mcp/oauth.ts";
import { resultPage as page } from "../result-page.ts";

export default defineHandler(async (event) => {
  let r;
  try {
    r = await handleCallback(event.url.searchParams);
  } catch (err) {
    console.error("[harness] mcp oauth callback failed", err);
    return page(false, "Authorization failed", "Something went wrong while finishing the authorization. Try again.", 500);
  }
  if (r.returnUrl) {
    const u = new URL(r.returnUrl);
    u.searchParams.set("mcp_flow", r.flowId);
    u.searchParams.set("mcp_status", r.ok ? "completed" : "failed");
    return new Response(null, { status: 303, headers: { location: u.href, "cache-control": "no-store", "referrer-policy": "no-referrer" } });
  }
  const what = r.serverName ? `MCP server “${r.serverName}”` : "the MCP server";
  return r.ok
    ? page(true, "Connected", `${what} is now authorized on all your devices. You can close this window.`, 200)
    : page(false, "Authorization failed", r.message, 400);
});
