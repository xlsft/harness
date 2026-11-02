/**
 * Minimal server-rendered device approval page (RFC 8628 "verification_uri").
 * GET  /device[?code=XXXX-XXXX]  sign-in form, code form, or approval form
 * POST /device                   approve / deny (form post, same-origin only, session cookie)
 */
import { defineHandler, type H3Event } from "h3";
import { hasUsers } from "../../auth/better-auth.ts";
import { config } from "../../env.ts";
import { decideDeviceAuthorization, findPendingByUserCode } from "../../services/devices.ts";
import { getPrincipal } from "../util.ts";
import { WORDMARK_SVG } from "../wordmark.ts";

function esc(s: string | null | undefined): string {
  return (s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

// Styled like the apps (docs/design/brand.md): black, monospace, square, blue accent; no external assets.
interface PageParts {
  heading: string;
  text: string;
  body?: string;
}

function page(title: string, parts: PageParts, status = 200): Response {
  const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<title>${esc(title)} · harness</title>
<style>
:root{--bg:#000;--line:#2b2b2b;--line-strong:#454545;--text:#d4d4d4;--muted:#8f8f8f;--white:#fff;--accent:#2b7fff;--bad:#ff2056}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--text);-webkit-font-smoothing:antialiased}
body{font:13px/1.6 "Cascadia Code","JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;min-height:100vh;display:flex;flex-direction:column;padding:0 20px}
main{flex:1;display:flex;align-items:center;justify-content:center}
.col{width:100%;max-width:420px;display:flex;flex-direction:column;gap:24px}
.head{display:flex;flex-direction:column;gap:16px}
.title{display:flex;flex-direction:column;gap:6px}
.wordmark{height:22px;width:auto;align-self:flex-start;color:var(--white)}
h1{margin:0;font-size:17px;line-height:1.4;font-weight:600;color:var(--white)}
p{margin:0}.title p{color:var(--text)}
form{display:flex;flex-direction:column;gap:12px}
label{display:flex;flex-direction:column;gap:6px;font-size:12px;color:var(--muted)}
input{height:36px;padding:0 10px;border:1px solid var(--line-strong);border-radius:0;background:var(--bg);color:var(--white);font:inherit;font-size:12.5px;outline:none}
input:-webkit-autofill{-webkit-box-shadow:0 0 0 40px var(--bg) inset;-webkit-text-fill-color:var(--white)}
input:focus{border-color:var(--accent)}
button{height:36px;border:1px solid var(--line);border-radius:0;background:transparent;color:var(--text);font:inherit;font-size:12.5px;cursor:pointer}
button:hover{color:var(--white)}
button.primary{background:var(--accent);border-color:var(--accent);color:var(--white)}
.row{display:flex;gap:8px}.row button{flex:1}
.code{font-size:24px;letter-spacing:.2em;color:var(--white);text-align:center;padding:14px;border:1px solid var(--line-strong);background:var(--bg)}
.code.small{font-size:15px;padding:8px}
dl{display:grid;grid-template-columns:auto 1fr;gap:4px 16px;margin:0;font-size:12px}dt{color:var(--muted)}dd{margin:0;color:var(--white)}
.err{color:var(--bad)}.err:empty{display:none}
footer{display:flex;justify-content:center;gap:16px;padding:16px 0;font-size:11.5px}
footer a{color:var(--muted);text-decoration:none}footer a:hover{color:var(--white)}
</style></head><body><main><div class="col"><div class="head">${WORDMARK_SVG}<div class="title"><h1>${parts.heading}</h1><p>${parts.text}</p></div></div>${parts.body ?? ""}</div></main>
<footer><a href="https://harness.xlsft.com">harness.xlsft.com ↗</a><a href="https://github.com/xlsft/harness">GitHub ↗</a></footer>
</body></html>`;
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

const SIGN_IN = { heading: "Sign in to connect a device", text: "A device asked to join your harness account. Sign in, then confirm it." };

function signInForm(code: string): string {
  return `${code ? `<div class="code small">${esc(code)}</div>` : ""}
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

const SETUP = { heading: "Create the first account", text: "This server has no accounts yet. The first one becomes its administrator; after that, sign-up closes and new people are added by an admin." };

function setupForm(code: string): string {
  return `${code ? `<div class="code small">${esc(code)}</div>` : ""}
<form id="f"><label>Name<input name="name" autocomplete="name" required></label>
<label>Email<input name="email" type="email" autocomplete="username" required></label>
<label>Password<input name="password" type="password" autocomplete="new-password" minlength="8" required></label>
<p class="err" id="err"></p><button class="primary">Create account</button></form>
<script>
document.getElementById('f').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const r = await fetch('/api/auth/sign-up/email', {method:'POST', headers:{'content-type':'application/json'},
    body: JSON.stringify({name: fd.get('name'), email: fd.get('email'), password: fd.get('password')})});
  if (r.ok) location.href = '/device' + (${JSON.stringify(code)} ? '?code=' + encodeURIComponent(${JSON.stringify(code)}) : '');
  else document.getElementById('err').textContent = (await r.json().catch(() => ({}))).message || 'Could not create the account';
});
</script>`;
}

const CONNECT = { heading: "Connect a device", text: "Enter the code the app or terminal shows you." };

function codeForm(error?: string): string {
  return `<form method="get" action="/device"><label>Code<input name="code" placeholder="XXXX-XXXX" autocomplete="off" required></label>
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
    if (!session) return page("Sign in", { ...SIGN_IN, body: signInForm("") }, 401);
    if (!sameOrigin(event)) return page("Error", { heading: "Request rejected", text: "Cross-origin form submission." }, 403);
    const form = new URLSearchParams(await event.req.text());
    const code = form.get("code") ?? "";
    const approve = form.get("action") === "approve";
    const ok = await decideDeviceAuthorization(code, session.user.id, approve);
    if (!ok) return page("Expired", { ...CONNECT, body: codeForm("This code is invalid or has expired.") }, 400);
    return page(
      approve ? "Approved" : "Denied",
      approve
        ? { heading: "Device connected", text: "You can close this tab and go back to the app." }
        : { heading: "Request denied", text: "The device was not connected." },
    );
  }

  const code = event.url.searchParams.get("code") ?? "";
  if (!session && !(await hasUsers())) return page("Set up", { ...SETUP, body: setupForm(code) });
  if (!session) return page("Sign in", { ...SIGN_IN, body: signInForm(code) });
  if (!code) return page("Connect a device", { ...CONNECT, body: codeForm() });
  const pending = await findPendingByUserCode(code);
  if (!pending) return page("Expired", { ...CONNECT, body: codeForm("This code is invalid or has expired.") }, 400);
  return page(
    "Approve device",
    {
      heading: "Connect this device?",
      text: `Signed in as ${esc(session.user.email)}. Check that the code matches the one in the app, and approve only if you started this yourself.`,
      body: `<div class="code">${esc(pending.userCode)}</div>
<dl><dt>Device</dt><dd>${esc(pending.deviceName)}</dd><dt>Client</dt><dd>${esc(pending.clientName)}</dd>
<dt>System</dt><dd>${esc([pending.os, pending.arch].filter(Boolean).join(" / "))}</dd></dl>
<form method="post" action="/device"><input type="hidden" name="code" value="${esc(pending.userCode)}">
<div class="row"><button name="action" value="deny">Deny</button><button class="primary" name="action" value="approve">Approve</button></div></form>`,
    },
  );
});
