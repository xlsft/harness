/**
 * Serves the built web UI (packages/ui, `nuxt generate`) at `/` when HARNESS_WEB_DIR names its directory, as the image does,
 * unless HARNESS_WEB_UI is off. The UI is a single page app, so any path that is no file gets its entry page. Without the
 * directory, or switched off, nothing changes: 404, and /api/health says no web UI.
 *
 * It runs in the browser with no secrets of its own. Agent work goes to a client the user runs (the page connects to it
 * with the client's token); signed in with the session cookie, it reaches the server's own administration services.
 */
import { createReadStream, statSync } from "node:fs";
import { extname, join, resolve, sep } from "node:path";
import { Readable } from "node:stream";
import { defineHandler } from "h3";

const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
  ".woff": "font/woff",
  ".txt": "text/plain; charset=utf-8",
  ".map": "application/json",
  ".wasm": "application/wasm",
};

// connect-src is open because the client a user connects to is wherever they run it (their machine, a reachable host).
const CSP = [
  "default-src 'self'",
  "script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'",
  "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
  "font-src 'self' data: https://fonts.gstatic.com",
  "img-src 'self' data: blob: https:",
  "media-src 'self' blob:",
  "connect-src * ws: wss:",
  "frame-src 'self' http://127.0.0.1:* http://localhost:*",
  "worker-src 'self' blob:",
  "base-uri 'self'",
  "form-action 'self'",
  "frame-ancestors 'none'",
].join("; ");

function notFound(): Response {
  return new Response(JSON.stringify({ error: { type: "not_found", message: "Not found" } }), { status: 404, headers: { "content-type": "application/json" } });
}

function file(path: string): { path: string; size: number } | undefined {
  try {
    const st = statSync(path);
    return st.isFile() ? { path, size: st.size } : undefined;
  } catch {
    return undefined;
  }
}

/** The directory of the web UI when the server serves one: HARNESS_WEB_DIR, unless HARNESS_WEB_UI is off. */
export function webDir(): string | undefined {
  const dir = process.env.HARNESS_WEB_DIR;
  const off = /^(off|0|false|no)$/i.test((process.env.HARNESS_WEB_UI ?? "").trim());
  return dir && !off ? dir : undefined;
}

/** The path the web UI is served at, for /api/health ("" when none): clients open sessions there (web_url). */
export function webPath(): string {
  const dir = webDir();
  return dir && (file(join(resolve(dir), "200.html")) ?? file(join(resolve(dir), "index.html"))) ? "/" : "";
}

export default defineHandler((event) => {
  const dir = webDir();
  if (!dir) return notFound();
  const root = resolve(dir);
  let rel: string;
  try {
    rel = decodeURIComponent(event.url.pathname);
  } catch {
    return notFound();
  }
  // Paths of the APIs never fall through to the app.
  if (rel.startsWith("/api/") || rel.startsWith("/scim/") || rel.startsWith("/harness.")) return notFound();
  const target = resolve(join(root, rel));
  const inside = target === root || target.startsWith(root + sep);
  const found = inside ? (file(target) ?? file(join(target, "index.html"))) : undefined;
  // An asset that is not there is a 404, not the app's page in its place. Anything else is a route of the app.
  const entry = found ?? (extname(rel) ? undefined : (file(join(root, "200.html")) ?? file(join(root, "index.html"))));
  if (!entry) return notFound();
  const type = TYPES[extname(entry.path).toLowerCase()] ?? "application/octet-stream";
  const immutable = entry.path.includes(`${sep}_nuxt${sep}`) && !entry.path.endsWith(".html");
  return new Response(event.req.method === "HEAD" ? null : (Readable.toWeb(createReadStream(entry.path)) as ReadableStream), {
    headers: {
      "content-type": type,
      "content-length": String(entry.size),
      "cache-control": immutable ? "public, max-age=31536000, immutable" : "no-cache",
      "x-content-type-options": "nosniff",
      "referrer-policy": "no-referrer",
      ...(type.startsWith("text/html") ? { "content-security-policy": CSP, "x-frame-options": "DENY" } : {}),
    },
  });
});
