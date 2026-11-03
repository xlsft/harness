import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { FileDiffStatus, GetSessionDiffResponseSchema } from '~/gen/harness/agent/v1/session_diff_pb'
import { sessionDiffView } from '~/lib/files/connect'
import { diffVersion, SessionDiffLoader, type SessionDiff } from '~/lib/files/sessionDiff'
import { MockTransport } from '~/lib/transport/mock'

function diff(version: string, extra: Partial<SessionDiff> = {}): SessionDiff {
  return { version, notModified: false, files: [], truncated: false, filesChanged: 0, additions: 0, deletions: 0, ...extra }
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

  it('has a version that depends on the content', () => {
    const f = { path: '/a', label: 'a', kind: 'M' as const, oldPath: '', add: 1, del: 0, binary: false, tooLarge: false, hunks: [] }
    expect(diffVersion([f])).toBe(diffVersion([{ ...f }]))
    expect(diffVersion([f])).not.toBe(diffVersion([{ ...f, add: 2 }]))
    expect(diffVersion([f])).not.toBe(diffVersion([f], true))
  })
})
