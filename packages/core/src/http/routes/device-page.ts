/**
 * Minimal server-rendered device approval page (RFC 8628 "verification_uri").
 * GET  /device[?code=XXXX-XXXX]  sign-in form, code form, or approval form
 * POST /device                   approve / deny (form post, same-origin only, session cookie)
 */
import { defineHandler, type H3Event } from "h3";
import { config } from "../../env.ts";
import { decideDeviceAuthorization, findPendingByUserCode } from "../../services/devices.ts";
import { getPrincipal } from "../util.ts";

function esc(s: string | null | undefined): string {
  return (s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

function page(title: string, body: string, status = 200): Response {
  const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${esc(title)} · harness</title>
<style>
:root{color-scheme:light dark;--fg:#1a1a1a;--bg:#fafafa;--muted:#666;--card:#fff;--line:#ddd;--accent:#c96442}
@media (prefers-color-scheme:dark){:root{--fg:#eee;--bg:#161616;--muted:#999;--card:#202020;--line:#333}}
body{font:15px/1.5 system-ui,sans-serif;background:var(--bg);color:var(--fg);margin:0;display:grid;place-items:center;min-height:100vh;padding:16px;box-sizing:border-box}
main{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:28px;max-width:420px;width:100%}
h1{font-size:20px;margin:0 0 16px}p{color:var(--muted)}label{display:block;margin:12px 0 4px}
input{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid var(--line);border-radius:8px;background:transparent;color:inherit;font:inherit}
.code{font:600 22px ui-monospace,monospace;letter-spacing:2px}
button{margin-top:16px;padding:9px 16px;border-radius:8px;border:1px solid var(--line);background:transparent;color:inherit;font:inherit;cursor:pointer}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
dl{display:grid;grid-template-columns:auto 1fr;gap:4px 12px}dt{color:var(--muted)}dd{margin:0}.err{color:#d33}
</style></head><body><main>${body}</main></body></html>`;
  return new Response(html, {
    status,
    headers: {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
      "x-frame-options": "DENY",
      "content-security-policy": "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'",
    },
  });
}

function signInForm(code: string): string {
  return `<h1>Sign in to harness</h1>
<p>Sign in to approve a device.</p>
<form id="f"><label>Email<input name="email" type="email" autocomplete="username" required></label>
<label>Password<input name="password" type="password" autocomplete="current-password" required></label>
<p class="err" id="err"></p><button class="primary">Sign in</button></form>
<script>
document.getElementById('f').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const r = await fetch('/api/auth/sign-in/email', {method:'POST', headers:{'content-type':'application/json'},
    body: JSON.stringify({email: fd.get('email'), password: fd.get('password')})});
  if (r.ok) location.href = '/device' + (${JSON.stringify(code)} ? '?code=' + encodeURIComponent(${JSON.stringify(code)}) : '');
  else document.getElementById('err').textContent = 'Sign-in failed';
});
</script>`;
}

function codeForm(error?: string): string {
  return `<h1>Connect a device</h1><p>Enter the code shown in your terminal.</p>
<form method="get" action="/device"><label>Code<input class="code" name="code" placeholder="XXXX-XXXX" autocomplete="off" required></label>
${error ? `<p class="err">${esc(error)}</p>` : ""}<button class="primary">Continue</button></form>`;
}

function sameOrigin(event: H3Event): boolean {
  const origin = event.req.headers.get("origin") ?? event.req.headers.get("referer");
  if (!origin) return false;
  try {
    const o = new URL(origin).origin;
    return o === new URL(config().publicUrl).origin || o === event.url.origin;
  } catch {
    return false;
  }
}

export default defineHandler(async (event) => {
  const principal = await getPrincipal(event);
  const session = principal?.via === "session" ? principal : undefined;

  if (event.req.method === "POST") {
    if (!session) return page("Sign in", signInForm(""), 401);
    if (!sameOrigin(event)) return page("Error", `<h1>Request rejected</h1><p>Cross-origin form submission.</p>`, 403);
    const form = new URLSearchParams(await event.req.text());
    const code = form.get("code") ?? "";
    const approve = form.get("action") === "approve";
    const ok = await decideDeviceAuthorization(code, session.user.id, approve);
    if (!ok) return page("Expired", codeForm("This code is invalid or has expired."), 400);
    return page(
      approve ? "Approved" : "Denied",
      approve
        ? `<h1>Device connected</h1><p>You can close this window and return to your terminal.</p>`
        : `<h1>Request denied</h1><p>The device was not connected.</p>`,
    );
  }

  const code = event.url.searchParams.get("code") ?? "";
  if (!session) return page("Sign in", signInForm(code));
  if (!code) return page("Connect a device", codeForm());
  const pending = await findPendingByUserCode(code);
  if (!pending) return page("Expired", codeForm("This code is invalid or has expired."), 400);
  return page(
    "Approve device",
    `<h1>Approve this device?</h1>
<p>Signed in as ${esc(session.user.email)}. Only approve if you started this sign-in yourself.</p>
<p class="code">${esc(pending.userCode)}</p>
<dl><dt>Device</dt><dd>${esc(pending.deviceName)}</dd><dt>Client</dt><dd>${esc(pending.clientName)}</dd>
<dt>System</dt><dd>${esc([pending.os, pending.arch].filter(Boolean).join(" / "))}</dd></dl>
<form method="post" action="/device"><input type="hidden" name="code" value="${esc(pending.userCode)}">
<button class="primary" name="action" value="approve">Approve</button> <button name="action" value="deny">Deny</button></form>`,
  );
});
