// Review of what a session already changed: each file in the native diff, against the baseline the client keeps. The
// rules (what the baseline is, what can be restored) are the client's; this only shows them.
import { basename } from 'node:path'
import * as vscode from 'vscode'
import type { Endpoint } from '../client/locate.ts'
import { revertSessionFile, sessionChanges, sessionFileBase } from '../client/rpc.ts'

export const BASE_SCHEME = 'harness-base'

interface Target {
  sessionId: string
  path: string
}

export interface ReviewHost {
  endpoint(): Promise<Endpoint>
  sessionId(): string
  log(line: string): void
}

function baseUri(t: Target): vscode.Uri {
  return vscode.Uri.from({ scheme: BASE_SCHEME, path: t.path, query: encodeURIComponent(t.sessionId) })
}

function targetOf(uri: vscode.Uri): Target {
  return { sessionId: decodeURIComponent(uri.query), path: uri.path }
}

export class ReviewController implements vscode.TextDocumentContentProvider, vscode.Disposable {
  private readonly disposables: vscode.Disposable[] = []
  private readonly changed = new vscode.EventEmitter<vscode.Uri>()
  readonly onDidChange = this.changed.event
  private readonly host: ReviewHost

  constructor(host: ReviewHost) {
    this.host = host
    this.disposables.push(
      this.changed,
      vscode.workspace.registerTextDocumentContentProvider(BASE_SCHEME, this),
      vscode.window.tabGroups.onDidChangeTabs(() => this.syncContext()),
      vscode.window.tabGroups.onDidChangeTabGroups(() => this.syncContext())
    )
  }

  async provideTextDocumentContent(uri: vscode.Uri): Promise<string> {
    const t = targetOf(uri)
    const base = await sessionFileBase(await this.host.endpoint(), t.sessionId, t.path)
    return base.binary ? '[binary or too large to show]' : base.content ?? ''
  }

  /** The list of changed files; picking one opens its diff. */
  async pick() {
    const sessionId = this.host.sessionId()
    if (!sessionId) {
      void vscode.window.showInformationMessage('Open a session in the xlsft`s harness panel first.')
      return
    }
    const files = await sessionChanges(await this.host.endpoint(), sessionId)
    if (!files.length) {
      void vscode.window.showInformationMessage('This session has no uncommitted changes.')
      return
    }
    const choice = await vscode.window.showQuickPick(files.map(f => ({
      label: f.displayPath || f.path,
      description: f.binary || f.tooLarge ? 'binary or large' : `+${f.additions ?? 0} -${f.deletions ?? 0}`,
      path: f.path
    })), { title: 'Session changes', placeHolder: 'Pick a file to review' })
    if (choice) await this.openDiff(sessionId, choice.path)
  }

  /** background: the agent changed it just now; the user's keyboard stays where it is, and the next one takes this tab. */
  async openDiff(sessionId: string, path: string, options: { background?: boolean } = {}) {
    const left = baseUri({ sessionId, path })
    this.changed.fire(left)
    const title = `${basename(path)} (session changes)`
    const show: vscode.TextDocumentShowOptions = { preview: true, preserveFocus: options.background ?? false, viewColumn: vscode.ViewColumn.One }
    await vscode.commands.executeCommand('vscode.diff', left, vscode.Uri.file(path), title, show)
    this.syncContext()
  }

  private activeTab(): { tab: vscode.Tab, target: Target } | undefined {
    const tab = vscode.window.tabGroups.activeTabGroup.activeTab
    if (tab?.input instanceof vscode.TabInputTextDiff && tab.input.original.scheme === BASE_SCHEME) return { tab, target: targetOf(tab.input.original) }
    return undefined
  }

  /** Keeps the file as it is: the review of this file is over. */
  async accept() {
    const a = this.activeTab()
    if (a) await vscode.window.tabGroups.close(a.tab, true)
  }

  /** Puts the file back. It may hold the user's own edits since the baseline, hence the question. */
  async reject(options: { confirmed?: boolean } = {}) {
    const a = this.activeTab()
    if (!a) return
    const name = basename(a.target.path)
    if (!options.confirmed) {
      const yes = 'Restore'
      const answer = await vscode.window.showWarningMessage(`Restore ${name}? All its uncommitted changes will be lost, yours included.`, { modal: true }, yes)
      if (answer !== yes) return
    }
    try {
      const res = await revertSessionFile(await this.host.endpoint(), a.target.sessionId, a.target.path)
      this.host.log(`${res.removed ? 'removed' : 'restored'} ${a.target.path}`)
      await vscode.window.tabGroups.close(a.tab, true)
    } catch (err) {
      void vscode.window.showErrorMessage(err instanceof Error ? err.message : String(err))
    }
  }

  private syncContext() {
    void vscode.commands.executeCommand('setContext', 'harness.reviewActive', this.activeTab() !== undefined)
  }

  dispose() {
    for (const d of this.disposables) d.dispose()
  }
}
