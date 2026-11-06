// A proposed edit in the native diff editor. The client holds a ShowDiff call open until the user answers here; closing
// the tab answers nothing, and the client's prompt stays in the panel.
import { basename } from 'node:path'
import * as vscode from 'vscode'
import { BridgeError, type ShowDiffRequest, type ShowDiffResponse } from '../protocol.ts'

export const DIFF_SCHEME = 'harness-diff'

interface OpenDiff {
  id: string
  before: string
  after: string
  left: vscode.Uri
  right: vscode.Uri
  settle: (r: ShowDiffResponse) => void
}

function uriOf(id: string, side: 'before' | 'after', path: string): vscode.Uri {
  // The file's name stays at the end, for its language and its icon.
  return vscode.Uri.from({ scheme: DIFF_SCHEME, path: `/${id}/${side}/${basename(path)}` })
}

export class DiffController implements vscode.TextDocumentContentProvider, vscode.Disposable {
  private readonly open = new Map<string, OpenDiff>()
  private readonly disposables: vscode.Disposable[] = []

  constructor() {
    this.disposables.push(
      vscode.workspace.registerTextDocumentContentProvider(DIFF_SCHEME, this),
      vscode.window.tabGroups.onDidChangeTabs(e => this.onTabs(e)),
      vscode.window.onDidChangeActiveTextEditor(() => this.syncContext()),
      vscode.window.tabGroups.onDidChangeTabGroups(() => this.syncContext())
    )
    this.syncContext()
  }

  provideTextDocumentContent(uri: vscode.Uri): string {
    const [, id, side] = uri.path.split('/')
    const d = id ? this.open.get(id) : undefined
    if (!d) return ''
    return side === 'before' ? d.before : d.after
  }

  /** Shows the change and waits for the answer. The signal closes it from the client's side. */
  show(req: ShowDiffRequest, signal: AbortSignal): Promise<ShowDiffResponse> {
    const id = req.diffId
    const path = req.path
    if (!id || !path) throw new BridgeError('invalid_argument', 'diffId and path are required')
    if (this.open.has(id)) throw new BridgeError('invalid_argument', `diff ${id} is already open`)
    return new Promise<ShowDiffResponse>((resolve, reject) => {
      const d: OpenDiff = {
        id, before: req.before ?? '', after: req.after ?? '', left: uriOf(id, 'before', path), right: uriOf(id, 'after', path),
        settle: (r) => {
          if (!this.open.delete(id)) return
          this.syncContext()
          void this.closeTab(d)
          resolve(r)
        }
      }
      this.open.set(id, d)
      signal.addEventListener('abort', () => d.settle({ outcome: 'DIFF_OUTCOME_DISMISSED' }), { once: true })
      const state = req.exists === false ? 'new file' : 'proposed edit'
      const title = `${basename(path)} (${state} by xlsft\`s harness)`
      vscode.commands.executeCommand('vscode.diff', d.left, d.right, title, { preview: false, preserveFocus: true } satisfies vscode.TextDocumentShowOptions).then(
        () => this.syncContext(),
        (err: unknown) => {
          this.open.delete(id)
          reject(err instanceof Error ? err : new Error(String(err)))
        }
      )
    })
  }

  /** The diff a command means: the one in front, else the only or latest one open. */
  private current(): OpenDiff | undefined {
    const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input
    if (input instanceof vscode.TabInputTextDiff && input.modified.scheme === DIFF_SCHEME) {
      const id = input.modified.path.split('/')[1]
      const d = id ? this.open.get(id) : undefined
      if (d) return d
    }
    return [...this.open.values()].at(-1)
  }

  accept() {
    this.current()?.settle({ outcome: 'DIFF_OUTCOME_ACCEPTED' })
  }

  reject(comment?: string) {
    this.current()?.settle({ outcome: 'DIFF_OUTCOME_REJECTED', comment: comment ?? '' })
  }

  async rejectWithNote() {
    const d = this.current()
    if (!d) return
    const note = await vscode.window.showInputBox({ title: 'Reject this edit', prompt: 'What should the agent do instead?', ignoreFocusOut: true })
    if (note === undefined) return
    d.settle({ outcome: 'DIFF_OUTCOME_REJECTED', comment: note })
  }

  count(): number {
    return this.open.size
  }

  private tabOf(d: OpenDiff): vscode.Tab | undefined {
    for (const group of vscode.window.tabGroups.all) {
      for (const tab of group.tabs) {
        if (tab.input instanceof vscode.TabInputTextDiff && tab.input.modified.toString() === d.right.toString()) return tab
      }
    }
    return undefined
  }

  private async closeTab(d: OpenDiff) {
    const tab = this.tabOf(d)
    if (tab) await vscode.window.tabGroups.close(tab, true)
  }

  private onTabs(e: vscode.TabChangeEvent) {
    this.syncContext()
    for (const tab of e.closed) {
      if (!(tab.input instanceof vscode.TabInputTextDiff) || tab.input.modified.scheme !== DIFF_SCHEME) continue
      const id = tab.input.modified.path.split('/')[1]
      const d = id ? this.open.get(id) : undefined
      d?.settle({ outcome: 'DIFF_OUTCOME_DISMISSED' })
    }
  }

  private syncContext() {
    void vscode.commands.executeCommand('setContext', 'harness.diffOpen', this.open.size > 0)
    const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input
    const active = input instanceof vscode.TabInputTextDiff && input.modified.scheme === DIFF_SCHEME
    void vscode.commands.executeCommand('setContext', 'harness.diffActive', active)
  }

  dispose() {
    for (const d of [...this.open.values()]) d.settle({ outcome: 'DIFF_OUTCOME_DISMISSED' })
    for (const d of this.disposables) d.dispose()
  }
}
