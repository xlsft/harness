import { describe, expect, it } from 'vitest'
import { createSessionState, sessionMeta } from '~/lib/session/reducer'
import { CHATS_GROUP, groupSessions } from '~/lib/session/tree'
import { MockTransport } from '~/lib/transport/mock'
import { newSessionFolder } from '~/utils/composer'

const s = (id: string, meta: Parameters<typeof createSessionState>[1] = {}) => createSessionState(id, { title: id, ...meta })
const names = (list: ReturnType<typeof groupSessions>) => list.map(g => `${g.name}: ${g.sessions.map(x => x.meta.id).join(' ')}`)

describe('sessions without a folder', () => {
  it('start the new-session screen without a folder unless ?cwd= asks for one', () => {
    expect(newSessionFolder({ new: '1' })).toBe('')
    expect(newSessionFolder({})).toBe('')
    expect(newSessionFolder({ new: '1', cwd: '/w/app' })).toBe('/w/app')
    expect(newSessionFolder({ cwd: ['/a', '/b'] })).toBe('')
  })

  it('are grouped under Chats, first, apart from a folder that happens to be named Chats', () => {
    const list = [
      s('a', { cwd: '/w/app', repo: 'github.com/x/app' }),
      s('c1', { cwd: '/h/.harness/chats/c1', noFolder: true }),
      s('b', { cwd: '/w/Chats' }),
      s('a2', { cwd: '/w/app/.harness/worktrees/x', worktree: { path: '/w/app/.harness/worktrees/x', branch: 'harness/x', baseRef: 'main', repositoryPath: '/w/app' } }),
      s('c2', { cwd: '/h/.harness/chats/c2', noFolder: true, parentId: 'c1' })
    ]
    const groups = groupSessions(list)
    expect(names(groups)).toEqual([`${CHATS_GROUP}: c1 c2`, 'app: a a2', 'Chats: b'])
    expect(groups[0]!.chats).toBe(true)
    expect(new Set(groups.map(g => g.key)).size).toBe(groups.length)
    expect(groupSessions(list.filter(x => !x.meta.noFolder)).some(g => g.chats)).toBe(false)
  })

  it('are created by the mock transport in a private directory, and forks get their own', async () => {
    const t = new MockTransport({ tick: 0 })
    const info = await t.createSession({ cwd: '' })
    expect(info.noFolder).toBe(true)
    expect(info.cwd).toMatch(new RegExp(`/\\.harness/chats/${info.id}$`))
    expect(info.gitBranch).toBe('')
    expect(sessionMeta(info).noFolder).toBe(true)
    await expect(t.createSession({ cwd: '', worktree: {} })).rejects.toThrow(/folder/)

    const fork = await t.createSession({ cwd: '', resumeSessionId: info.id, fork: true })
    expect(fork.noFolder).toBe(true)
    expect(fork.cwd).not.toBe(info.cwd)
    expect(fork.parentSessionId).toBe(info.id)

    const inFolder = await t.createSession({ cwd: '/Users/xlsft/Projects/xlsft/harness' })
    expect(inFolder.noFolder).toBe(false)
    expect((await t.listSessions()).filter(i => i.noFolder).map(i => i.id)).toEqual([info.id, fork.id])
  })
})
