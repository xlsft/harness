/** Small standalone HTML result page for browser redirect targets (OAuth callbacks, app setup). */

export function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

/** `bodyHtml` is put in as it is: the caller escapes it. */
export function resultPage(ok: boolean, title: string, message: string, status: number, bodyHtml = ""): Response {
  const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${esc(title)} · xlsft\`s harness</title>
<style>
:root{color-scheme:dark;--fg:#d4d4d4;--bg:#000;--muted:#8f8f8f;--ok:#00bc7d;--err:#ff2056}
@media (prefers-color-scheme:light){:root{color-scheme:light;--fg:#2b2b2b;--bg:#fff;--muted:#6b6b6b;--ok:#00965f;--err:#e0003f}}
body{font:13px/1.6 "Cascadia Code","JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--bg);color:var(--fg);margin:0;display:grid;place-items:center;min-height:100vh;padding:16px;box-sizing:border-box;-webkit-font-smoothing:antialiased}
main{max-width:420px;width:100%}
h1{font-size:17px;line-height:1.4;font-weight:600;margin:0 0 6px;color:${ok ? "var(--ok)" : "var(--err)"}}p{color:var(--muted);margin:0}
a{color:inherit}dl{display:grid;grid-template-columns:auto 1fr;gap:4px 16px;margin:16px 0;font-size:12px}dt{color:var(--muted)}dd{margin:0;overflow-wrap:anywhere}
pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:11.5px}summary{cursor:pointer;color:var(--muted)}
</style></head><body><main><h1>${esc(title)}</h1><p>${esc(message)}</p>${bodyHtml}</main></body></html>`;
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
