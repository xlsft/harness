// The page of the panel: the UI's generated index.html made loadable in a webview. No editor API here, so it runs under
// plain Node in tests.

export interface PageOptions {
  /** index.html of the IDE build of packages/ui. */
  html: string
  /** The webview URI of the folder it came from, without a trailing slash. */
  assets: string
  /** webview.cspSource. */
  cspSource: string
  nonce: string
  url: string
  token: string
  folder: string
  kind: 'light' | 'dark'
}

function jsonInScript(value: unknown): string {
  // Nothing in it can close the script tag. The two line separators JSON allows are legal in a script since ES2019.
  return JSON.stringify(value).replace(/</g, '\\u003c')
}

/** The content security policy: the page's own files, and the one client it was given. */
export function contentSecurityPolicy(cspSource: string, nonce: string, url: string): string {
  const u = new URL(url)
  const http = u.origin
  const ws = `ws://${u.host}`
  return [
    `default-src 'none'`,
    `script-src 'nonce-${nonce}' ${cspSource}`,
    `style-src 'unsafe-inline' ${cspSource}`,
    `font-src ${cspSource} data:`,
    `img-src ${cspSource} ${http} data: blob:`,
    `media-src ${http} blob:`,
    `connect-src ${http} ${ws} ${cspSource}`,
    `frame-src ${http} blob: data:`,
    `worker-src blob:`,
    `base-uri 'none'`,
    `form-action 'none'`
  ].join('; ')
}

export function renderPage(o: PageOptions): string {
  const csp = contentSecurityPolicy(o.cspSource, o.nonce, o.url)
  const boot = `const vscode=acquireVsCodeApi();window.__HARNESS_IDE__=${jsonInScript({ url: o.url, token: o.token, folder: o.folder, kind: o.kind })};window.__HARNESS_IDE__.post=m=>vscode.postMessage(m);document.addEventListener('securitypolicyviolation',e=>vscode.postMessage({type:'blocked',directive:e.effectiveDirective,uri:e.blockedURI}));`
  return o.html
    // The UI's paths are rooted at a server the webview does not have.
    .replaceAll('"/_nuxt/', `"${o.assets}/_nuxt/`)
    // At runtime it finds its chunks through this.
    .replace('cdnURL:""', `cdnURL:${JSON.stringify(`${o.assets}/`)}`)
    .replace(/<script(?![^>]*\bnonce=)/g, `<script nonce="${o.nonce}"`)
    .replace(/<head>/, `<head><meta http-equiv="Content-Security-Policy" content="${csp}"><script nonce="${o.nonce}">${boot}</script>`)
}
