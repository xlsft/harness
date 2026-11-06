// The session panel: packages/ui in a webview. Its messages are lib/ide.ts on the UI's side.
import { randomBytes } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import * as vscode from 'vscode'
import type { Endpoint } from '../client/locate.ts'
import { renderPage } from './html.ts'

export type ToPage
  = | { type: 'add-context', path: string, lineStart: number, lineEnd: number }
    | { type: 'add-text', text: string }
    | { type: 'new-session', folder: string }
    | { type: 'interrupt' }
    | { type: 'focus-composer' }
    | { type: 'theme', kind: 'light' | 'dark' }
    | { type: 'probe' }

export type FromPage
  = | { type: 'ready', connected: boolean, error: string }
    | { type: 'session', id: string, cwd: string }
    | { type: 'open-file', path: string, line: number, endLine: number }
    | { type: 'open-diff', sessionId: string, path: string }
    | { type: 'connection-lost' }
    | { type: 'context-added', mention: string }
    | { type: 'probe-result', text: string, elements: number }
    | { type: 'blocked', directive: string, uri: string }

export interface PanelHost {
  extensionUri: vscode.Uri
  endpoint(): Promise<Endpoint>
  folder(): string
  log(line: string): void
  onMessage(m: FromPage): void
}

const themeKind = (): 'light' | 'dark' => {
  const k = vscode.window.activeColorTheme.kind
  return k === vscode.ColorThemeKind.Light || k === vscode.ColorThemeKind.HighContrastLight ? 'light' : 'dark'
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

export class SessionPanel implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined
  private ready = false
  private queue: ToPage[] = []
  private readonly host: PanelHost
  private readonly themeSub: vscode.Disposable

  constructor(host: PanelHost) {
    this.host = host
    this.themeSub = vscode.window.onDidChangeActiveColorTheme(() => this.post({ type: 'theme', kind: themeKind() }))
  }

  get isOpen() {
    return this.panel !== undefined
  }

  /** Opens the panel, or brings it forward. */
  async open(preserveFocus = false) {
    if (this.panel) {
      this.panel.reveal(undefined, preserveFocus)
      return
    }
    const ui = vscode.Uri.joinPath(this.host.extensionUri, 'media', 'ui')
    const panel = vscode.window.createWebviewPanel('harness.session', 'xlsft`s harness', { viewColumn: vscode.ViewColumn.Beside, preserveFocus }, {
      enableScripts: true,
      // The UI holds the session's live state; a hidden tab keeps it instead of reconnecting.
      retainContextWhenHidden: true,
      localResourceRoots: [ui]
    })
    this.panel = panel
    this.ready = false
    panel.onDidDispose(() => {
      this.panel = undefined
      this.ready = false
    })
    panel.webview.onDidReceiveMessage((m: FromPage) => {
      if (m.type === 'ready') {
        this.ready = true
        for (const queued of this.queue.splice(0)) void panel.webview.postMessage(queued)
      }
      this.host.onMessage(m)
    })
    await this.render(panel, ui)
  }

  /** Renders the page against the current client: on opening, and after the client was restarted. */
  async reload() {
    if (!this.panel) return
    this.ready = false
    await this.render(this.panel, vscode.Uri.joinPath(this.host.extensionUri, 'media', 'ui'))
  }

  private async render(panel: vscode.WebviewPanel, ui: vscode.Uri) {
    const index = vscode.Uri.joinPath(ui, 'index.html')
    if (!existsSync(index.fsPath)) {
      panel.webview.html = this.message('The UI is missing from this build. Run <code>node scripts/build.mjs --ui</code> in packages/vscode.')
      return
    }
    let ep: Endpoint
    try {
      ep = await this.host.endpoint()
    } catch (err) {
      panel.webview.html = this.message(escapeHtml(err instanceof Error ? err.message : String(err)))
      return
    }
    // The client may be on the other side of a port forward (remote development).
    const port = Number(new URL(ep.url).port)
    panel.webview.options = { ...panel.webview.options, portMapping: [{ webviewPort: port, extensionHostPort: port }] }
    panel.webview.html = renderPage({
      html: readFileSync(index.fsPath, 'utf8'),
      assets: panel.webview.asWebviewUri(ui).toString().replace(/\/$/, ''),
      cspSource: panel.webview.cspSource,
      nonce: randomBytes(16).toString('hex'),
      url: ep.url,
      token: ep.token,
      folder: this.host.folder(),
      kind: themeKind()
    })
  }

  private message(html: string): string {
    return `<!DOCTYPE html><html><body style="font-family: var(--vscode-font-family); padding: 2em"><p>${html}</p></body></html>`
  }

  /** Sends to the page, after it is up if it isn't yet. */
  post(m: ToPage) {
    if (this.panel && this.ready) void this.panel.webview.postMessage(m)
    else this.queue.push(m)
  }

  dispose() {
    this.themeSub.dispose()
    this.panel?.dispose()
  }
}
