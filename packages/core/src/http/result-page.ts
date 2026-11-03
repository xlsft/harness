/** Small standalone HTML result page for browser redirect targets (OAuth callbacks, app setup). */

export function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

export function resultPage(ok: boolean, title: string, message: string, status: number): Response {
  const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${esc(title)} · xlsft\`s harness</title>
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
