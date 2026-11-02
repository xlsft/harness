/**
 * GET /api/mcp/oauth/callback?code=...&state=... — redirect target of MCP OAuth flows (McpService.StartOAuth).
 * The flow is identified by `state` alone (the browser may not be signed in to harness, e.g. a URL opened
 * from the terminal). Finishes the flow, then redirects to the flow's return_url or shows a result page.
 */
import { defineHandler } from "h3";
import { handleCallback } from "../../mcp/oauth.ts";

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

function page(ok: boolean, title: string, message: string, status: number): Response {
  const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${esc(title)} · harness</title>
<style>
:root{color-scheme:light dark;--fg:#1a1a1a;--bg:#fafafa;--muted:#666;--card:#fff;--line:#ddd;--ok:#2a7a3b;--err:#c33}
@media (prefers-color-scheme:dark){:root{--fg:#eee;--bg:#161616;--muted:#999;--card:#202020;--line:#333;--ok:#5fbf73;--err:#f66}}
body{font:15px/1.5 system-ui,sans-serif;background:var(--bg);color:var(--fg);margin:0;display:grid;place-items:center;min-height:100vh;padding:16px;box-sizing:border-box}
main{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:28px;max-width:420px;width:100%}
h1{font-size:20px;margin:0 0 12px;color:${ok ? "var(--ok)" : "var(--err)"}}p{color:var(--muted);margin:0}
</style></head><body><main><h1>${esc(title)}</h1><p>${esc(message)}</p></main></body></html>`;
  return new Response(html, {
    status,
    headers: {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
      "referrer-policy": "no-referrer",
      "x-frame-options": "DENY",
      "content-security-policy": "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'",
    },
  });
}

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
