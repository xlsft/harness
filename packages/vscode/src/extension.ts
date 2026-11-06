// The extension's wiring: finds or starts the client, serves the bridge, and connects the panel and the commands. The
// design is in docs/design/ide.md.
import { existsSync } from 'node:fs'
import { isAbsolute } from 'node:path'
import * as vscode from 'vscode'
import { removeLock, writeLock } from './bridge/lock.ts'
import { startBridge, type BridgeServer } from './bridge/server.ts'
import { findBinary, harnessHome, loopbackUrl, readAnnounced, type Endpoint } from './client/locate.ts'
import { ClientStartError, startClient, type StartedClient } from './client/process.ts'
import { answers, sessionChanges } from './client/rpc.ts'
import { getDiagnostics } from './editor/diagnostics.ts'
import { DiffController } from './editor/diff.ts'
import { ReviewController } from './editor/review.ts'
import { formatCommand, TerminalWatcher } from './editor/terminal.ts'
import { SessionPanel, type FromPage } from './panel/panel.ts'
import type { BridgeHandlers, ReadBufferResponse } from './protocol.ts'

/** What the extension gives back: the extension tests read it. */
export interface ExtensionApi {
  lockFile: string
  endpoint(): Promise<Endpoint>
  panelReady: Promise<{ connected: boolean, error: string }>
  sessionId(): string
  whenSession(): Promise<string>
  openDiffs(): number
  changedFiles(): number
  contextAdded(): string[]
  blocked(): string[]
  probePanel(): Promise<{ text: string, elements: number }>
  review(sessionId: string, path: string): Promise<void>
}

const firstFolder = () => vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? ''
const folders = () => (vscode.workspace.workspaceFolders ?? []).filter(f => f.uri.scheme === 'file').map(f => f.uri.fsPath)

export async function activate(context: vscode.ExtensionContext): Promise<ExtensionApi> {
  const output = vscode.window.createOutputChannel('xlsft`s harness')
  const log = (line: string) => output.appendLine(line)
  const home = harnessHome()
  const diffs = new DiffController()
  const terminals = new TerminalWatcher()
  const status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 50)
  status.command = 'harness.reviewChanges'
  context.subscriptions.push(output, diffs, terminals, status)

  // ---- the client ----

  let started: StartedClient | undefined
  let connecting: Promise<Endpoint> | undefined
  let lockFile = ''

  async function findOrStart(): Promise<Endpoint> {
    const config = vscode.workspace.getConfiguration('harness')
    const explicit = config.get<string>('agentUrl', '')
    if (explicit) {
      const url = loopbackUrl(explicit)
      const token = process.env['HARNESS_AGENT_TOKEN'] ?? ''
      if (!url) throw new Error('harness.agentUrl must be a loopback http address.')
      const ep = { url, token }
      if (!(await answers(ep))) throw new Error(`No client answers at ${url} with the token in HARNESS_AGENT_TOKEN.`)
      log(`using the client at ${url}`)
      return ep
    }
    const announced = readAnnounced(home)
    if (announced && await answers(announced)) {
      log(`using the running client at ${announced.url}`)
      return announced
    }
    const binary = findBinary(config.get<string>('clientPath', ''))
    if (!binary) throw new Error('harness-client was not found. Install it, or set harness.clientPath.')
    log(`starting ${binary}`)
    started = await startClient({
      binary, cwd: firstFolder() || undefined, lockFile, onOutput: log,
      onExit: (code) => {
        log(`the client exited (${code})`)
        started = undefined
        connecting = undefined
      }
    })
    log(`the client listens at ${started.endpoint.url}`)
    return started.endpoint
  }

  /** The client's endpoint. A failure is not kept, so the next use tries again. */
  function endpoint(): Promise<Endpoint> {
    connecting ??= findOrStart().catch((err: unknown) => {
      connecting = undefined
      throw err
    })
    return connecting
  }

  async function restartClient() {
    started?.stop()
    started = undefined
    connecting = undefined
    await panel.reload()
  }

  // ---- the bridge ----

  const handlers: BridgeHandlers = {
    ping: () => ({ name: vscode.env.appName, version: vscode.version, workspaceFolders: folders() }),
    showDiff: (req, signal) => diffs.show(req, signal),
    getDiagnostics: req => getDiagnostics(req),
    openFile: req => openFile(req.path ?? '', req.line ?? 0, req.endLine ?? 0),
    readBuffer: (req): ReadBufferResponse => {
      const doc = vscode.workspace.textDocuments.find(d => d.uri.scheme === 'file' && d.uri.fsPath === req.path)
      return doc ? { open: true, content: doc.getText(), dirty: doc.isDirty } : { open: false }
    },
    editApplied: (req) => {
      void refreshChanges()
      // Most sessions approve edits by themselves: this is where the user gets to see them.
      const show = vscode.workspace.getConfiguration('harness').get<boolean>('showAppliedEdits', true)
      if (show && !req.reviewed && req.sessionId && req.path) void review.openDiff(req.sessionId, req.path, { background: true }).catch((err: unknown) => log(String(err)))
    }
  }
  const bridge: BridgeServer = await startBridge(handlers)
  const writeOurLock = () => writeLock(home, { port: bridge.port, token: bridge.token, pid: process.pid, name: vscode.env.appName, folders: folders() })
  lockFile = writeOurLock()
  // A terminal of this window reaches this window, whatever folder its session is in. The token stays in the file.
  context.environmentVariableCollection.persistent = false
  context.environmentVariableCollection.replace('HARNESS_IDE_LOCK', lockFile)
  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(() => writeOurLock()), {
    dispose: () => {
      removeLock(lockFile)
      started?.stop()
      void bridge.close()
    }
  })

  // ---- the panel ----

  let currentSession = ''
  let changed = 0
  const added: string[] = []
  const blocked: string[] = []
  const probes: ((r: { text: string, elements: number }) => void)[] = []
  let resolveReady: (r: { connected: boolean, error: string }) => void = () => {}
  const panelReady = new Promise<{ connected: boolean, error: string }>(r => (resolveReady = r))
  const sessionWaiters: ((id: string) => void)[] = []

  async function refreshChanges() {
    if (!currentSession) {
      changed = 0
      status.hide()
      return
    }
    try {
      changed = (await sessionChanges(await endpoint(), currentSession)).length
    } catch (err) {
      log(`could not read the session's changes: ${String(err)}`)
      return
    }
    status.text = `$(diff) ${changed} changed`
    status.tooltip = 'Review what this xlsft`s harness session changed'
    if (changed > 0) status.show()
    else status.hide()
  }

  const onPageMessage = (m: FromPage) => {
    switch (m.type) {
      case 'ready':
        if (m.error) log(`the panel reports: ${m.error}`)
        resolveReady({ connected: m.connected, error: m.error })
        break
      case 'session':
        currentSession = m.id
        if (m.id) for (const resolve of sessionWaiters.splice(0)) resolve(m.id)
        void refreshChanges()
        break
      case 'open-file':
        void openFile(m.path, m.line, m.endLine)
        break
      case 'open-diff':
        void review.openDiff(m.sessionId, m.path)
        break
      case 'probe-result':
        for (const resolve of probes.splice(0)) resolve({ text: m.text, elements: m.elements })
        break
      case 'blocked':
        log(`the panel's content security policy blocked ${m.directive}: ${m.uri}`)
        blocked.push(`${m.directive} ${m.uri}`)
        break
      case 'context-added':
        added.push(m.mention)
        break
      case 'connection-lost':
        log('the panel lost the client; restarting it')
        void restartClient()
        break
    }
  }

  // A client that can't start shows up when the panel asks for it; say why, with a way out.
  const explain = async (err: unknown) => {
    log(String(err))
    const signIn = err instanceof ClientStartError && err.signIn
    const action = await vscode.window.showErrorMessage(err instanceof Error ? err.message : String(err), signIn ? 'Sign In' : 'Show Log')
    if (action === 'Sign In') await vscode.commands.executeCommand('harness.signIn')
    if (action === 'Show Log') output.show()
  }

  const panel = new SessionPanel({ extensionUri: context.extensionUri, endpoint, folder: firstFolder, log, onMessage: onPageMessage, onNoClient: err => void explain(err) })
  const review = new ReviewController({ endpoint, sessionId: () => currentSession, log })
  context.subscriptions.push(panel, review)

  async function openFile(path: string, line: number, endLine: number) {
    // From the transcript this may be any word that looks like a path: only what exists opens.
    if (!isAbsolute(path) || !existsSync(path)) {
      log(`not opening ${path}: no such file`)
      return
    }
    const selection = line > 0 ? new vscode.Range(line - 1, 0, Math.max(endLine, line) - 1, endLine > line ? Number.MAX_SAFE_INTEGER : 0) : undefined
    const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(path))
    // The panel stays where it is: the file opens next to it.
    await vscode.window.showTextDocument(doc, { viewColumn: vscode.ViewColumn.One, preview: true, selection })
  }

  // ---- commands ----

  async function addToContext() {
    const editor = vscode.window.activeTextEditor
    if (!editor || editor.document.uri.scheme !== 'file') {
      void vscode.window.showInformationMessage('Open a file on disk to add it to the context.')
      return
    }
    await panel.open(true)
    const s = editor.selection
    if (s.isEmpty) {
      panel.post({ type: 'add-context', path: editor.document.uri.fsPath, lineStart: 0, lineEnd: 0 })
    } else {
      // A selection that ends at the start of a line does not include that line.
      const last = s.end.character === 0 && s.end.line > s.start.line ? s.end.line - 1 : s.end.line
      panel.post({ type: 'add-context', path: editor.document.uri.fsPath, lineStart: s.start.line + 1, lineEnd: last + 1 })
    }
    panel.post({ type: 'focus-composer' })
  }

  async function addFile(uri?: vscode.Uri, all?: vscode.Uri[]) {
    const picked = all?.length ? all : uri ? [uri] : vscode.window.activeTextEditor ? [vscode.window.activeTextEditor.document.uri] : []
    const files = picked.filter(u => u.scheme === 'file')
    if (!files.length) return
    await panel.open(true)
    for (const u of files) panel.post({ type: 'add-context', path: u.fsPath, lineStart: 0, lineEnd: 0 })
  }

  const register = (id: string, run: (...args: never[]) => unknown) => context.subscriptions.push(vscode.commands.registerCommand(id, run))
  register('harness.open', () => panel.open())
  register('harness.newSession', async () => {
    await panel.open()
    panel.post({ type: 'new-session', folder: firstFolder() })
  })
  register('harness.addToContext', addToContext)
  register('harness.addFile', addFile)
  register('harness.addTerminalOutput', async () => {
    const last = terminals.lastCommand()
    if (!last) {
      void vscode.window.showInformationMessage('No finished command to add. It needs a terminal with shell integration.')
      return
    }
    await panel.open(true)
    panel.post({ type: 'add-text', text: formatCommand(last) })
  })
  register('harness.interrupt', () => panel.post({ type: 'interrupt' }))
  register('harness.reviewChanges', () => review.pick())
  register('harness.openInTerminal', () => {
    const terminal = vscode.window.createTerminal({ name: 'xlsft`s harness' })
    terminal.show()
    terminal.sendText('harness')
  })
  register('harness.signIn', async () => {
    const server = await vscode.window.showInputBox({ title: 'Sign in to xlsft`s harness', prompt: 'The address of your harness server', placeHolder: 'https://harness.example.com', ignoreFocusOut: true })
    if (!server) return
    const binary = findBinary(vscode.workspace.getConfiguration('harness').get<string>('clientPath', ''))
    if (!binary) {
      void vscode.window.showErrorMessage('harness-client was not found. Install it, or set harness.clientPath.')
      return
    }
    // The device flow runs in a terminal so that the code is in front of the user; no secret passes through here.
    const terminal = vscode.window.createTerminal({ name: 'xlsft`s harness sign-in', shellPath: binary, shellArgs: ['login', server] })
    terminal.show()
    const sub = vscode.window.onDidCloseTerminal((t) => {
      if (t !== terminal) return
      sub.dispose()
      void restartClient()
    })
    context.subscriptions.push(sub)
  })
  register('harness.restartClient', restartClient)
  register('harness.showLog', () => output.show())
  register('harness.diff.accept', () => diffs.accept())
  register('harness.diff.reject', () => diffs.reject())
  register('harness.diff.rejectWithNote', () => diffs.rejectWithNote())
  register('harness.review.accept', () => review.accept())
  register('harness.review.reject', (options?: { confirmed?: boolean }) => review.reject(options))

  return {
    lockFile,
    endpoint,
    panelReady,
    sessionId: () => currentSession,
    whenSession: () => currentSession ? Promise.resolve(currentSession) : new Promise(r => sessionWaiters.push(r)),
    openDiffs: () => diffs.count(),
    changedFiles: () => changed,
    contextAdded: () => added,
    blocked: () => blocked,
    probePanel: () => new Promise((resolve) => {
      probes.push(resolve)
      panel.post({ type: 'probe' })
    }),
    review: (sessionId, path) => review.openDiff(sessionId, path)
  }
}

export function deactivate() {
  // Everything is on context.subscriptions.
}
