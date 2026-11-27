// Runs inside the editor (run.mjs starts it). Every wait is on a condition. What it exercises is real: the editor, the
// panel's page, the client and the bridge. Only the model and its server are fakes.
const assert = require('node:assert/strict')
const { existsSync, readFileSync, rmSync, statSync, writeFileSync } = require('node:fs')
const { join } = require('node:path')
const vscode = require('vscode')

const workspace = process.env.HARNESS_TEST_WORKSPACE
const AGENT = 'harness.agent.v1.AgentService'

async function until(what, check, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const got = await check()
    if (got) return got
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`)
    await new Promise(r => setTimeout(r, 50))
  }
}

async function rpc(ep, service, method, body = {}) {
  const res = await fetch(`${ep.url}/${service}/${method}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', 'Authorization': `Bearer ${ep.token}` },
    body: JSON.stringify(body)
  })
  const text = await res.text()
  if (!res.ok) throw new Error(`${method}: ${res.status} ${text}`)
  return text ? JSON.parse(text) : {}
}

// A session and the events of its Subscribe stream (Connect streaming: a 5-byte header before each JSON message).
// The mode is named: a real client's default is auto, which approves edits inside the folder without asking.
async function openSession(ep, mode = 'PERMISSION_MODE_DEFAULT') {
  const created = await rpc(ep, AGENT, 'CreateSession', { cwd: workspace, permissionMode: mode })
  const id = created.session.id
  const events = []
  const stop = new AbortController()
  const body = Buffer.alloc(5 + Buffer.byteLength(JSON.stringify({ sessionId: id })))
  body.write(JSON.stringify({ sessionId: id }), 5)
  body.writeUInt32BE(body.length - 5, 1)
  const res = await fetch(`${ep.url}/${AGENT}/Subscribe`, {
    method: 'POST', signal: stop.signal, body,
    headers: { 'Content-Type': 'application/connect+json', 'Connect-Protocol-Version': '1', 'Authorization': `Bearer ${ep.token}` }
  })
  assert.equal(res.status, 200)
  void (async () => {
    let buf = Buffer.alloc(0)
    try {
      for await (const chunk of res.body) {
        buf = Buffer.concat([buf, chunk])
        while (buf.length >= 5 && buf.length >= 5 + buf.readUInt32BE(1)) {
          const size = buf.readUInt32BE(1)
          const msg = JSON.parse(buf.subarray(5, 5 + size).toString())
          buf = buf.subarray(5 + size)
          if (msg.event) events.push(msg.event)
        }
      }
    } catch {
      // Stopped at the end of a test.
    }
  })()
  return {
    id,
    events,
    send: text => rpc(ep, AGENT, 'SendUserMessage', { sessionId: id, content: [{ text: { text } }] }),
    close: () => stop.abort(),
    finishedTurns: () => events.filter(e => e.turnFinished).length,
    prompt: () => events.findLast(e => e.permissionRequest)?.permissionRequest,
    resolved: () => events.findLast(e => e.permissionResolved)?.permissionResolved,
    reply: () => events.filter(e => e.assistantMessage || e.message).map(e => JSON.stringify(e)).join('\n')
  }
}

const diffTabs = () => vscode.window.tabGroups.all.flatMap(g => g.tabs).filter(t => t.input instanceof vscode.TabInputTextDiff && t.input.modified.scheme === 'harness-diff')

const tests = {
  async 'the extension finds a client and announces itself'(api) {
    const ep = await api.endpoint()
    assert.match(ep.url, /^http:\/\/127\.0\.0\.1:\d+$/)
    const lock = JSON.parse(readFileSync(api.lockFile, 'utf8'))
    assert.deepEqual(lock.folders, [workspace])
    assert.equal(lock.pid, process.pid)
    assert.equal(statSync(api.lockFile).mode & 0o777, 0o600)
  },

  async 'the panel runs packages/ui and connects to the client'(api) {
    await vscode.commands.executeCommand('harness.open')
    const ready = await api.panelReady
    assert.deepEqual(ready, { connected: true, error: '' })
    // Not only connected: it drew itself, and its policy refused none of its own files.
    const page = await until('the page to draw', async () => {
      const p = await api.probePanel()
      return p.elements > 50 && p.text.trim() ? p : undefined
    })
    assert.ok(page.text.trim().length > 0, 'the page has text')
    assert.deepEqual(api.blocked(), [])
  },

  async 'an edit opens in the native diff, and accepting writes it'(api) {
    const s = await openSession(await api.endpoint())
    const path = join(workspace, 'accepted.txt')
    await s.send('ide:write accepted.txt hello from the agent')
    const [tab] = await until('the diff tab', () => { const t = diffTabs(); return t.length ? t : undefined })
    assert.match(tab.label, /accepted\.txt/)
    const after = await vscode.workspace.openTextDocument(tab.input.modified)
    assert.equal(after.getText(), 'hello from the agent')
    assert.ok(tab.input.modified.path.endsWith('accepted.txt'), 'the name stays, for the language')
    assert.equal(existsSync(path), false, 'nothing is written before the answer')
    await until('the prompt', () => s.prompt())

    await vscode.commands.executeCommand('harness.diff.accept')
    await until('the turn', () => s.finishedTurns() === 1)
    assert.equal(readFileSync(path, 'utf8'), 'hello from the agent')
    assert.equal(s.resolved().resolvedBy, 'editor')
    await until('the tab to close', () => diffTabs().length === 0)
    s.close()
  },

  async 'rejecting leaves the file alone and stops the turn'(api) {
    const s = await openSession(await api.endpoint())
    await s.send('ide:write rejected.txt no')
    await until('the diff tab', () => diffTabs().length === 1)
    await vscode.commands.executeCommand('harness.diff.reject')
    await until('the turn', () => s.finishedTurns() === 1)
    assert.equal(existsSync(join(workspace, 'rejected.txt')), false)
    assert.equal(s.resolved().decision, 'DECISION_DENY')
    s.close()
  },

  async 'closing the tab leaves the prompt, and answering it there still works'(api) {
    const ep = await api.endpoint()
    const s = await openSession(ep)
    await s.send('ide:write closed.txt maybe')
    await until('the diff tab', () => diffTabs().length === 1)
    const prompt = await until('the prompt', () => s.prompt())
    await vscode.window.tabGroups.close(diffTabs()[0])
    await until('the diff to be gone', () => api.openDiffs() === 0)
    assert.equal(s.finishedTurns(), 0, 'the turn goes on waiting')
    await rpc(ep, AGENT, 'RespondToPermission', { sessionId: s.id, requestId: prompt.requestId, decision: 'DECISION_ALLOW' })
    await until('the turn', () => s.finishedTurns() === 1)
    assert.equal(readFileSync(join(workspace, 'closed.txt'), 'utf8'), 'maybe')
    s.close()
  },

  async 'answering in the panel closes the diff'(api) {
    const ep = await api.endpoint()
    const s = await openSession(ep)
    await s.send('ide:write fromthepanel.txt ok')
    await until('the diff tab', () => diffTabs().length === 1)
    const prompt = await until('the prompt', () => s.prompt())
    await rpc(ep, AGENT, 'RespondToPermission', { sessionId: s.id, requestId: prompt.requestId, decision: 'DECISION_ALLOW' })
    await until('the tab to close', () => diffTabs().length === 0)
    await until('the turn', () => s.finishedTurns() === 1)
    s.close()
  },

  async 'an edit nobody was asked about opens for review, and rejecting restores the file'(api) {
    const ep = await api.endpoint()
    const file = join(workspace, 'silent.txt')
    const s = await openSession(ep, 'PERMISSION_MODE_AUTO')
    await s.send('ide:write silent.txt applied without asking')
    await until('the turn', () => s.finishedTurns() === 1)
    assert.equal(s.prompt(), undefined, 'nothing asked')
    assert.equal(readFileSync(file, 'utf8'), 'applied without asking')
    const tab = await until('the diff', () => vscode.window.tabGroups.all.flatMap(g => g.tabs).find(t => t.input instanceof vscode.TabInputTextDiff && t.input.original.scheme === 'harness-base' && t.input.modified.fsPath === file))
    assert.equal(tab.isPreview, true, 'the next edit takes the same tab')
    await tab.group.tabs.length
    await vscode.window.tabGroups.close(vscode.window.tabGroups.all.flatMap(g => g.tabs).filter(t => t !== tab && t.input instanceof vscode.TabInputTextDiff && t.input.original.scheme === 'harness-base'))
    await until('the review commands', () => {
      const a = vscode.window.tabGroups.activeTabGroup.activeTab
      return a?.input instanceof vscode.TabInputTextDiff && a.input.original.scheme === 'harness-base'
    })
    await vscode.commands.executeCommand('harness.review.reject', { confirmed: true })
    await until('the file to be removed', () => !existsSync(file))
    s.close()
  },

  async 'an edit someone answered for does not open again'(api) {
    const ep = await api.endpoint()
    const file = join(workspace, 'answered.txt')
    const s = await openSession(ep)
    await s.send('ide:write answered.txt yes')
    await until('the diff tab', () => diffTabs().length === 1)
    await vscode.commands.executeCommand('harness.diff.accept')
    await until('the turn', () => s.finishedTurns() === 1)
    assert.equal(readFileSync(file, 'utf8'), 'yes')
    // The next silent edit goes through the same path, so once it has opened, this one had its chance.
    const later = await openSession(ep, 'PERMISSION_MODE_AUTO')
    await later.send('ide:write marker.txt done')
    const reviewOf = name => vscode.window.tabGroups.all.flatMap(g => g.tabs).find(t => t.input instanceof vscode.TabInputTextDiff && t.input.original.scheme === 'harness-base' && t.input.modified.fsPath === join(workspace, name))
    await until('the later diff', () => reviewOf('marker.txt'))
    assert.equal(reviewOf('answered.txt'), undefined)
    s.close()
    later.close()
  },

  async 'a deleted file can be reviewed and brought back'(api) {
    const ep = await api.endpoint()
    const file = join(workspace, 'deleted.txt')
    writeFileSync(file, 'keep me\n')
    const s = await openSession(ep)
    await rpc(ep, AGENT, 'GetSessionDiff', { sessionId: s.id })
    rmSync(file)
    await until('the change to show', async () => {
      const diff = await rpc(ep, AGENT, 'GetSessionDiff', { sessionId: s.id, base: { kind: 'DIFF_BASE_KIND_UNCOMMITTED' } })
      return (diff.files ?? []).some(f => f.path === file)
    })
    await api.review(s.id, file)
    await until('the diff', () => {
      const t = vscode.window.tabGroups.activeTabGroup.activeTab
      return t?.input instanceof vscode.TabInputTextDiff && t.input.original.scheme === 'harness-base' && t.input.original.fsPath === file
    })
    await vscode.commands.executeCommand('harness.review.reject', { confirmed: true })
    await until('the file to be back', () => existsSync(file) && readFileSync(file, 'utf8') === 'keep me\n')
    s.close()
  },

  async 'the problems panel reaches the agent'(api) {
    const file = join(workspace, 'broken.ts')
    writeFileSync(file, 'let x: number = "s"\n')
    const collection = vscode.languages.createDiagnosticCollection('harness-test')
    const d = new vscode.Diagnostic(new vscode.Range(0, 4, 0, 5), 'a problem only the editor knows', vscode.DiagnosticSeverity.Error)
    d.source = 'fake-lint'
    d.code = 'L1'
    collection.set(vscode.Uri.file(file), [d])
    const s = await openSession(await api.endpoint())
    await s.send('ide:diagnostics')
    await until('the turn', () => s.finishedTurns() === 1)
    const all = JSON.stringify(s.events)
    assert.ok(all.includes('a problem only the editor knows'), all.slice(-2000))
    assert.ok(all.includes(':1:5: error [fake-lint L1]'), 'with position, severity and source')
    collection.dispose()
    s.close()
  },

  async 'a selection mention carries the live buffer'(api) {
    const file = join(workspace, 'live.txt')
    writeFileSync(file, 'one\ntwo\nthree\n')
    const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(file))
    const editor = await vscode.window.showTextDocument(doc)
    await editor.edit(b => b.replace(new vscode.Range(1, 0, 1, 3), 'unsaved'))
    assert.ok(doc.isDirty)
    const s = await openSession(await api.endpoint())
    await s.send('explain @live.txt#L2')
    await until('the turn', () => s.finishedTurns() === 1)
    const all = JSON.stringify(s.events)
    assert.ok(all.includes('unsaved'), all.slice(-2000))
    s.close()
    await vscode.commands.executeCommand('workbench.action.revertAndCloseActiveEditor')
  },

  async 'the selection keystroke reaches the composer'(api) {
    const file = join(workspace, 'selected.txt')
    writeFileSync(file, 'a\nb\nc\nd\n')
    const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(file))
    const editor = await vscode.window.showTextDocument(doc)
    editor.selection = new vscode.Selection(1, 0, 3, 0)
    const before = api.contextAdded().length
    await vscode.commands.executeCommand('harness.addToContext')
    const mention = await until('the page to take the mention', () => api.contextAdded()[before])
    assert.equal(mention, 'selected.txt#L2-3')
    await vscode.commands.executeCommand('workbench.action.closeActiveEditor')
  },

  async 'opening a file at a line works through the bridge'(api) {
    const file = join(workspace, 'opened.txt')
    writeFileSync(file, 'a\nb\nc\nd\ne\n')
    const lock = JSON.parse(readFileSync(api.lockFile, 'utf8'))
    await rpc({ url: `http://127.0.0.1:${lock.port}`, token: lock.token }, 'harness.ide.v1.EditorBridgeService', 'OpenFile', { path: file, line: 4 })
    const editor = await until('the file', () => vscode.window.visibleTextEditors.find(e => e.document.uri.fsPath === file))
    assert.equal(editor.selection.start.line, 3)
    await vscode.commands.executeCommand('workbench.action.closeActiveEditor')
  },

  async 'reviewing a session change shows the baseline, and rejecting restores it'(api) {
    const ep = await api.endpoint()
    const file = join(workspace, 'reviewed.txt')
    writeFileSync(file, 'before\n')
    const s = await openSession(ep, 'PERMISSION_MODE_ACCEPT_EDITS')
    // The session measures the folder when it first looks; then the file changes under it, as with a shell command.
    await rpc(ep, AGENT, 'GetSessionDiff', { sessionId: s.id })
    writeFileSync(file, 'after\n')
    await until('the change to show', async () => {
      const diff = await rpc(ep, AGENT, 'GetSessionDiff', { sessionId: s.id, base: { kind: 'DIFF_BASE_KIND_UNCOMMITTED' } })
      return (diff.files ?? []).some(f => f.path === file)
    })
    await api.review(s.id, file)
    const tab = await until('the diff', () => {
      const t = vscode.window.tabGroups.activeTabGroup.activeTab
      return t?.input instanceof vscode.TabInputTextDiff && t.input.original.scheme === 'harness-base' ? t : undefined
    })
    const base = await vscode.workspace.openTextDocument(tab.input.original)
    assert.equal(base.getText(), 'before\n')
    assert.equal(tab.input.modified.fsPath, file)
    await vscode.commands.executeCommand('harness.review.reject', { confirmed: true })
    await until('the file to be restored', () => readFileSync(file, 'utf8') === 'before\n')
    s.close()
  }
}

exports.run = async function run() {
  const extension = vscode.extensions.getExtension('xlsft.harness-vscode')
  assert.ok(extension, 'the extension is not installed')
  const api = await extension.activate()
  const failures = []
  for (const [name, test] of Object.entries(tests)) {
    const started = Date.now()
    try {
      await test(api)
      console.log(`  ok   ${name} (${Date.now() - started} ms)`)
    } catch (err) {
      failures.push(name)
      console.error(`  FAIL ${name}\n${err && err.stack ? err.stack : err}`)
    }
  }
  if (failures.length) throw new Error(`${failures.length} of ${Object.keys(tests).length} failed: ${failures.join('; ')}`)
}
