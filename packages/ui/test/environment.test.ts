import { create } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { EnvironmentErrorsSchema, EnvironmentSchema, GetFolderInfoResponseSchema } from '~/gen/harness/agent/v1/agent_pb'
import { environmentMessage, environmentView, folderInfoView, parseErrorOf } from '~/lib/environment/connect'
import { parseEnv } from '~/lib/environment/dotenv'
import { EnvironmentParseError, FOLDER_CHANGED, sessionWebUrl } from '~/lib/environment/types'
import { MockTransport } from '~/lib/transport/mock'
import { MAIN_SESSION } from '~/lib/transport/scenario'
import type { SessionEvent } from '~/gen/harness/agent/v1/agent_pb'

const HARNESS = '/Users/xlsft/Projects/xlsft/harness'

describe('environment mapping', () => {
  it('maps the environment, folder info and drafts', () => {
    const v = environmentView(create(EnvironmentSchema, {
      folder: '/p', name: 'api', worktree: true, baseRef: 'main', env: 'A=1', exists: true, updatedAt: '2026-11-03T10:00:00Z',
      variableNames: ['A'], additionalFolders: [{ path: '/q', worktree: true, baseRef: 'dev', exists: false, gitBranch: '' }]
    }))
    expect(v).toEqual({
      folder: '/p', name: 'api', worktree: true, baseRef: 'main', env: 'A=1', exists: true, readOnly: false, updatedAt: '2026-11-03T10:00:00Z',
      variableNames: ['A'], additionalFolders: [{ path: '/q', worktree: true, baseRef: 'dev', exists: false, gitBranch: '' }]
    })
    expect(environmentView(undefined)).toMatchObject({ folder: '', exists: false, readOnly: false, additionalFolders: [] })
    expect(environmentMessage({ folder: '/p', additionalFolders: [{ path: '/q' }] })).toEqual({
      folder: '/p', name: '', worktree: false, baseRef: '', env: '', additionalFolders: [{ path: '/q', worktree: false, baseRef: '' }]
    })
    const fi = folderInfoView(create(GetFolderInfoResponseSchema, { path: '/p', exists: true, repositoryPath: '/p', currentBranch: 'main', defaultBranch: 'main', remoteUrl: 'git@github.com:a/b.git', webUrl: 'https://github.com/a/b', repo: 'github.com/a/b' }))
    expect(fi.webUrl).toBe('https://github.com/a/b')
  })

  it('turns EnvironmentErrors details into an EnvironmentParseError', () => {
    const err = new ConnectError('invalid', Code.InvalidArgument, undefined, [{ desc: EnvironmentErrorsSchema, value: { errors: [{ line: 2, message: 'bad name' }, { line: 5, message: 'unterminated' }] } }])
    const out = parseErrorOf(err)
    expect(out).toBeInstanceOf(EnvironmentParseError)
    expect((out as EnvironmentParseError).errors).toEqual([{ line: 2, message: 'bad name' }, { line: 5, message: 'unterminated' }])
    const other = new ConnectError('nope', Code.FailedPrecondition)
    expect(parseErrorOf(other)).toBe(other)
  })

  it('builds session web links', () => {
    expect(sessionWebUrl('https://h.example.com/', 'abc 1')).toBe('https://h.example.com/?session=abc%201')
    expect(sessionWebUrl('', 'abc')).toBe('')
  })
})

describe('parseEnv', () => {
  it('follows the client rules', () => {
    const { vars, errors } = parseEnv('# c\nexport A=1 # x\nB="l1\\nl2"\nC=\'m\nn\'\nA=2\n')
    expect(errors).toEqual([])
    expect(vars).toEqual([{ name: 'A', value: '2', line: 6 }, { name: 'B', value: 'l1\nl2', line: 3 }, { name: 'C', value: 'm\nn', line: 4 }])
    expect(parseEnv('1A=x\nNOVAL\n__HARNESS_CMD=1\nD="open').errors.map(e => e.line)).toEqual([1, 2, 3, 4])
  })
})

describe('MockEnvironmentApi', () => {
  it('stores, validates and applies environments', async () => {
    const t = new MockTransport({ tick: 0 })
    const env = t.environment()
    const sid = MAIN_SESSION
    const folder = (await t.listSessions()).find(s => s.id === sid)!.cwd.replace(/^~/, '/Users/xlsft')
    expect(await env.get({ sessionId: sid })).toMatchObject({ folder, exists: false })
    await expect(env.update({ folder, env: 'OK=1\n2X=3' })).rejects.toBeInstanceOf(EnvironmentParseError)
    const v = await env.update({ folder, env: 'A=1' }, { validateOnly: true })
    expect(v.environment.exists).toBe(false)
    const up = await env.update({ folder, name: 'harness', env: 'TOKEN="x"\n', additionalFolders: [{ path: '/Users/xlsft/Projects/xlsft/dashboard' }, { path: '/nope' }] })
    expect(up.appliedSessionIds).toContain(sid)
    expect(up.environment).toMatchObject({ exists: true, name: 'harness', variableNames: ['TOKEN'] })
    expect(up.environment.additionalFolders.map(f => [f.exists, f.gitBranch])).toEqual([[true, 'develop'], [false, '']])
    const add = await env.addSessionDirectory(sid, '/Users/xlsft/Documents')
    expect(add.environment.additionalFolders.map(f => f.path)).toContain('/Users/xlsft/Documents')
    expect(add.environment.env).toBe('TOKEN="x"\n')

    const remote = (await t.listSessions()).find(s => s.remote)!
    expect(await env.get({ sessionId: remote.id })).toMatchObject({ readOnly: true, env: '' })
    await expect(env.update({ folder }, { sessionId: remote.id })).rejects.toThrow(/failed_precondition/)
    await expect(env.setSessionFolder(remote.id, folder)).rejects.toThrow(/failed_precondition/)

    const info = await env.folderInfo(`${HARNESS}/packages`)
    expect(info).toMatchObject({ repositoryPath: HARNESS, webUrl: 'https://github.com/xlsft/harness', remoteUrl: 'git@github.com:xlsft/harness.git' })
    expect((await env.folderInfo('/Users/xlsft/Desktop')).webUrl).toBe('')
  })

  it('moves an idle session and ends its subscriptions', async () => {
    const t = new MockTransport({ tick: 0 })
    const env = t.environment()
    const idle = (await t.listSessions()).find(s => s.id !== MAIN_SESSION && !s.remote && !s.worktree && s.status !== 2)!
    const got: SessionEvent[] = []
    let ended = false
    t.subscribe(idle.id, 1e9, {
      onEvent: ev => got.push(ev),
      onEnd: () => {
        ended = true
      }
    })
    await new Promise(r => setTimeout(r, 0))
    await expect(env.setSessionFolder(MAIN_SESSION, '/Users/xlsft/Desktop')).rejects.toThrow(/working/)
    const moved = await env.setSessionFolder(idle.id, '/Users/xlsft/Desktop')
    expect(moved.cwd).toBe('/Users/xlsft/Desktop')
    expect(ended).toBe(true)
    expect(got.map(e => e.event.case === 'sessionEnded' && e.event.value.reason)).toContain(FOLDER_CHANGED)
    expect((await t.listSessions()).find(s => s.id === idle.id)!.cwd).toBe('/Users/xlsft/Desktop')
    await expect(env.setSessionFolder(idle.id, '/Users/xlsft/nowhere')).rejects.toThrow(/not_found/)
  })
})
