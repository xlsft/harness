import { describe, expect, it, vi } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { DiffBaseKind, FileDiffStatus, GetSessionDiffResponseSchema, ListDiffBasesResponseSchema } from '~/gen/harness/agent/v1/session_diff_pb'
import { diffBaseMessage, diffBasesView, sessionDiffView } from '~/lib/files/connect'
import { diffVersion, SessionDiffLoader, type SessionDiff } from '~/lib/files/sessionDiff'
import { MockTransport } from '~/lib/transport/mock'

function diff(version: string, extra: Partial<SessionDiff> = {}): SessionDiff {
  return { version, notModified: false, files: [], truncated: false, filesChanged: 0, additions: 0, deletions: 0, baseLabel: 'main', headLabel: 'working tree', baseKind: 'all', large: false, refreshAfterMs: 5000, ...extra }
}

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('SessionDiffLoader', () => {
  function setup() {
    const asked: string[] = []
    const answers: ReturnType<typeof deferred<SessionDiff>>[] = []
    const changes: string[] = []
    const errors: unknown[] = []
    const loader = new SessionDiffLoader({
      fetch: (known) => {
        asked.push(known)
        const d = deferred<SessionDiff>()
        answers.push(d)
        return d.promise
      },
      onChange: d => changes.push(d.version),
      onError: e => errors.push(e)
    })
    return { asked, answers, changes, errors, loader }
  }

  it('reports a new version, and drops a repeat', async () => {
    const { asked, answers, changes, loader } = setup()
    let p = loader.load()
    answers[0]!.resolve(diff('v1'))
    await p
    p = loader.load()
    answers[1]!.resolve(diff('v1', { notModified: true }))
    await p
    p = loader.load()
    answers[2]!.resolve(diff('v1'))
    await p
    p = loader.load()
    answers[3]!.resolve(diff('v2'))
    await p
    expect(changes).toEqual(['v1', 'v2'])
    expect(asked).toEqual(['', 'v1', 'v1', 'v1'])
  })

  it('runs one request at a time, and once more for what was asked meanwhile', async () => {
    const { answers, changes, loader } = setup()
    const p = loader.load()
    void loader.load()
    void loader.load()
    expect(answers).toHaveLength(1)
    answers[0]!.resolve(diff('v1'))
    await p
    await Promise.resolve()
    expect(answers).toHaveLength(2)
    answers[1]!.resolve(diff('v2'))
    await new Promise(r => setTimeout(r))
    expect(changes).toEqual(['v1', 'v2'])
    expect(answers).toHaveLength(2)
  })

  it('keeps the last state on a failure, and recovers', async () => {
    const { answers, changes, errors, loader } = setup()
    let p = loader.load()
    answers[0]!.resolve(diff('v1'))
    await p
    p = loader.load()
    answers[1]!.reject(new Error('[unavailable] gone'))
    await p
    expect(errors).toHaveLength(1)
    expect(changes).toEqual(['v1'])
    p = loader.load()
    answers[2]!.resolve(diff('v1', { notModified: true }))
    await p
    expect(changes).toEqual(['v1'])
  })

  it('ignores an answer from before a reset (another session)', async () => {
    const { asked, answers, changes, errors, loader } = setup()
    const stale = loader.load()
    loader.reset('known')
    const p = loader.load()
    answers[0]!.resolve(diff('old'))
    await stale
    answers[1]!.resolve(diff('new'))
    await p
    expect(asked).toEqual(['', 'known'])
    expect(changes).toEqual(['new'])
    expect(errors).toEqual([])
  })
})

describe('SessionDiffLoader with a base and a pace', () => {
  function setup(answers: (base: unknown) => SessionDiff) {
    const asked: unknown[][] = []
    const changes: string[] = []
    let visible = true
    const loader = new SessionDiffLoader({
      fetch: async (known, base) => {
        asked.push([known, base])
        return answers(base)
      },
      onChange: d => changes.push(d.version),
      onError: () => {},
      shouldPoll: () => visible
    })
    return { asked, changes, loader, hide: (hidden: boolean) => {
      visible = !hidden
    } }
  }

  it('passes the base, and a new base starts over', async () => {
    const { asked, changes, loader } = setup(() => diff('v1'))
    await loader.load()
    expect(asked[0]).toEqual(['', undefined])
    loader.setBase({ kind: 'ref', ref: 'main' })
    await loader.load()
    expect(asked[1]).toEqual(['', { kind: 'ref', ref: 'main' }])
    // The same answer counts as new: it is of another base.
    expect(changes).toEqual(['v1', 'v1'])
    // The same base again changes nothing.
    loader.setBase({ kind: 'ref', ref: 'main' })
    await loader.load()
    expect(asked[2]).toEqual(['v1', { kind: 'ref', ref: 'main' }])
  })

  it('drops an answer for the base it left', async () => {
    const answers: ((d: SessionDiff) => void)[] = []
    const changes: string[] = []
    const loader = new SessionDiffLoader({
      fetch: () => new Promise(r => answers.push(r)),
      onChange: d => changes.push(d.version),
      onError: () => {}
    })
    const stale = loader.load()
    loader.setBase({ kind: 'uncommitted' })
    const p = loader.load()
    answers[0]!(diff('old', { refreshAfterMs: 30000 }))
    await stale
    expect(loader.refreshAfterMs).toBe(5000)
    answers[1]!(diff('new'))
    await p
    expect(changes).toEqual(['new'])
  })

  it('polls at the pace the answers set, not-modified ones included', async () => {
    vi.useFakeTimers()
    try {
      let answer = diff('v1', { large: true, refreshAfterMs: 30000 })
      const { asked, loader, hide } = setup(() => answer)
      loader.start()
      expect(asked).toHaveLength(0)
      await vi.advanceTimersByTimeAsync(5000) // the pace before any answer
      expect(asked).toHaveLength(1)
      expect(loader.refreshAfterMs).toBe(30000)
      await vi.advanceTimersByTimeAsync(29999)
      expect(asked).toHaveLength(1)
      answer = diff('v1', { notModified: true, large: false, refreshAfterMs: 5000 })
      await vi.advanceTimersByTimeAsync(1)
      expect(asked).toHaveLength(2)
      await vi.advanceTimersByTimeAsync(5000)
      expect(asked).toHaveLength(3)
      // Asked for at once: the pace counts from that answer.
      await vi.advanceTimersByTimeAsync(4000)
      await loader.load()
      expect(asked).toHaveLength(4)
      await vi.advanceTimersByTimeAsync(4000)
      expect(asked).toHaveLength(4)
      // Hidden: no asking, and it resumes.
      hide(true)
      await vi.advanceTimersByTimeAsync(20000)
      expect(asked).toHaveLength(4)
      hide(false)
      await vi.advanceTimersByTimeAsync(5000)
      expect(asked).toHaveLength(5)
      loader.stop()
      await vi.advanceTimersByTimeAsync(60000)
      expect(asked).toHaveLength(5)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('the base over the wire', () => {
  it('builds the request', () => {
    expect(diffBaseMessage()).toBeUndefined()
    expect(diffBaseMessage({ kind: 'all' })).toEqual({ kind: DiffBaseKind.ALL })
    expect(diffBaseMessage({ kind: 'uncommitted' })).toEqual({ kind: DiffBaseKind.UNCOMMITTED })
    expect(diffBaseMessage({ kind: 'ref', ref: 'main' })).toEqual({ kind: DiffBaseKind.REF, ref: 'main' })
    expect(diffBaseMessage({ kind: 'commit', sha: 'abcd' })).toEqual({ kind: DiffBaseKind.COMMIT, sha: 'abcd' })
  })

  it('maps the answers', () => {
    const v = sessionDiffView(create(GetSessionDiffResponseSchema, { baseLabel: 'main', headLabel: 'working tree', baseKind: DiffBaseKind.REF, large: true, refreshAfterMs: 30000 }))
    expect(v).toMatchObject({ baseLabel: 'main', headLabel: 'working tree', baseKind: 'ref', large: true, refreshAfterMs: 30000 })
    const b = diffBasesView(create(ListDiffBasesResponseSchema, {
      inGit: true,
      branches: [{ name: 'main', current: true }],
      defaultBaseRef: 'main',
      defaultBaseLabel: 'main',
      defaultBaseSha: 'abc',
      commits: [{ sha: 'def', subject: 'A commit', committedAt: '2026-01-02T03:04:05Z' }]
    }))
    expect(b).toEqual({
      inGit: true, branches: [{ name: 'main', current: true, remote: false }], branchesTruncated: false, defaultRef: 'main', defaultLabel: 'main', defaultSha: 'abc',
      commits: [{ sha: 'def', subject: 'A commit', committedAt: '2026-01-02T03:04:05Z' }], commitsTruncated: false
    })
  })
})

describe('sessionDiffView', () => {
  it('maps the response', async () => {
    const res = create(GetSessionDiffResponseSchema, {
      version: 'abc',
      additions: 1,
      filesChanged: 2,
      files: [
        { path: '/p/new.go', displayPath: 'new.go', status: FileDiffStatus.RENAMED, oldPath: '/p/old.go', additions: 1, hunks: [{ oldStart: 1, oldLines: 0, newStart: 1, newLines: 1, lines: ['+x'], header: 'func a()' }] },
        { path: '/p/big', status: FileDiffStatus.DELETED, tooLarge: true }
      ]
    })
    const v = sessionDiffView(res)
    expect(v.version).toBe('abc')
    expect(v.filesChanged).toBe(2)
    expect(v.files[0]).toEqual({
      path: '/p/new.go', label: 'new.go', kind: 'R', oldPath: '/p/old.go', add: 1, del: 0, binary: false, tooLarge: false,
      hunks: [{ oldStart: 1, oldLines: 0, newStart: 1, newLines: 1, lines: ['+x'], header: 'func a()' }]
    })
    expect(v.files[1]).toMatchObject({ path: '/p/big', label: '/p/big', kind: 'D', tooLarge: true, hunks: [] })
  })
})

describe('the mock transport', () => {
  async function session() {
    const t = new MockTransport({ tick: 0 })
    const info = await t.createSession({ cwd: '/u/proj' })
    return { t, id: info.id }
  }

  it('reports what shell commands changed, and notices the next change', async () => {
    const { t, id } = await session()
    const none = await t.sessionDiff(id)
    expect(none.files).toEqual([])

    // A Bash command edits README.md and creates a file: no Edit call in the transcript.
    const before = (await t.files().read(id, 'README.md')).text
    t.files().write(id, 'README.md', `${before}one more line\n`)
    t.files().write(id, 'made.txt', 'a\nb\n')
    const first = await t.sessionDiff(id)
    expect(first.files.map(f => [f.label, f.kind, f.add, f.del])).toEqual([['README.md', 'M', 1, 0], ['made.txt', 'A', 2, 0]])
    expect(first.files[0]!.path).toBe('/u/proj/README.md')
    expect(first.additions).toBe(3)
    expect(first.filesChanged).toBe(2)

    const same = await t.sessionDiff(id, { ifNoneMatch: first.version })
    expect(same.notModified).toBe(true)
    expect(same.files).toEqual([])
    expect(same.additions).toBe(3)

    t.files().write(id, 'README.md', `${before}other line\nand another\n`)
    const later = await t.sessionDiff(id, { ifNoneMatch: first.version })
    expect(later.notModified).toBe(false)
    expect(later.version).not.toBe(first.version)
    expect(later.files.find(f => f.label === 'README.md')!.add).toBe(2)

    // Back to where it started: not a change any more.
    t.files().write(id, 'README.md', before)
    expect((await t.sessionDiff(id)).files.map(f => f.label)).toEqual(['made.txt'])
  })

  it('reports the session\'s Edit calls', async () => {
    const t = new MockTransport({ tick: 0 })
    const list = await t.listSessions()
    const reports = await Promise.all(list.filter(s => !s.remote).map(s => t.sessionDiff(s.id)))
    expect(reports.some(r => r.files.length > 0)).toBe(true)
    for (const r of reports) expect(r.filesChanged).toBe(r.files.length)
  })

  it('fails for a session of another machine', async () => {
    const t = new MockTransport({ tick: 0 })
    const remote = (await t.listSessions()).find(s => s.remote)
    expect(remote).toBeTruthy()
    await expect(t.sessionDiff(remote!.id)).rejects.toThrow(/failed_precondition/)
  })

  it('compares with each base', async () => {
    const t = new MockTransport({ tick: 0 })
    const list = await t.listSessions()
    let id = ''
    for (const s of list.filter(s => !s.remote)) if ((await t.sessionDiff(s.id)).files.length) id = s.id
    expect(id).toBeTruthy()
    t.files().write(id, 'made.txt', 'a\nb\n')

    const all = await t.sessionDiff(id)
    expect(all).toMatchObject({ baseKind: 'all', baseLabel: 'main', headLabel: 'working tree', large: false, refreshAfterMs: 5000 })
    const uncommitted = await t.sessionDiff(id, { base: { kind: 'uncommitted' } })
    expect(uncommitted.files.map(f => f.label)).toEqual(['made.txt'])
    expect(uncommitted).toMatchObject({ baseKind: 'uncommitted', baseLabel: 'HEAD' })
    expect(uncommitted.version).not.toBe(all.version)
    expect(await t.sessionDiff(id, { base: { kind: 'ref', ref: 'release/1.4' } })).toMatchObject({ baseKind: 'ref', baseLabel: 'release/1.4' })
    await expect(t.sessionDiff(id, { base: { kind: 'ref', ref: 'nope' } })).rejects.toThrow(/not_found/)
    await expect(t.sessionDiff(id, { base: { kind: 'ref', ref: '--output=x' } })).rejects.toThrow(/invalid_argument/)

    const bases = await t.listDiffBases(id)
    expect(bases.inGit).toBe(true)
    expect(bases.defaultLabel).toBe('main')
    expect(bases.commits.length).toBeGreaterThan(0)
    const commit = await t.sessionDiff(id, { base: { kind: 'commit', sha: bases.commits[0]!.sha } })
    expect(commit.baseKind).toBe('commit')
    expect(commit.headLabel).toBe(bases.commits[0]!.sha.slice(0, 7))
    expect(commit.files.some(f => f.label === 'made.txt')).toBe(false)
    await expect(t.sessionDiff(id, { base: { kind: 'commit', sha: 'deadbeef' } })).rejects.toThrow(/not_found/)
  })

  it('answers a large diff without hunks, and each file on demand', async () => {
    const { t, id } = await session()
    for (let i = 0; i < 41; i++) t.files().write(id, `many/f${i}.txt`, 'a\nb\n')
    const large = await t.sessionDiff(id)
    expect(large).toMatchObject({ large: true, refreshAfterMs: 30000, filesChanged: 41, additions: 82 })
    expect(large.files.every(f => f.hunks.length === 0)).toBe(true)
    const file = await t.sessionFileDiff(id, large.files[0]!.path)
    expect(file.hunks.length).toBe(1)
    await expect(t.sessionFileDiff(id, '/etc/hosts')).rejects.toThrow(/not_found/)
    expect(await t.sessionDiff(id, { ifNoneMatch: large.version })).toMatchObject({ notModified: true, large: true, refreshAfterMs: 30000, additions: 82, files: [] })
  })

  it('has a version that depends on the content', () => {
    const f = { path: '/a', label: 'a', kind: 'M' as const, oldPath: '', add: 1, del: 0, binary: false, tooLarge: false, hunks: [] }
    expect(diffVersion([f])).toBe(diffVersion([{ ...f }]))
    expect(diffVersion([f])).not.toBe(diffVersion([{ ...f, add: 2 }]))
    expect(diffVersion([f])).not.toBe(diffVersion([f], true))
  })
})
