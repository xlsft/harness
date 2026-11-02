import { describe, expect, it } from 'vitest'
import { AGENT_TOKEN_STORAGE_KEY, resolveAgentToken } from '~/lib/transport/token'

function memoryStorage(init: Record<string, string> = {}) {
  const data = new Map(Object.entries(init))
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v)
  }
}

describe('resolveAgentToken', () => {
  it('takes the query parameter, stores it for the tab and strips it from the URL', () => {
    const storage = memoryStorage()
    let replaced = ''
    const token = resolveAgentToken('from-config', {
      href: 'http://localhost:3210/?session=s1&agentToken=abc123#x',
      storage,
      replaceUrl: (u) => {
        replaced = u
      }
    })
    expect(token).toBe('abc123')
    expect(storage.data.get(AGENT_TOKEN_STORAGE_KEY)).toBe('abc123')
    expect(replaced).toBe('http://localhost:3210/?session=s1#x')
  })

  it('falls back to sessionStorage, then to runtime config', () => {
    const storage = memoryStorage({ [AGENT_TOKEN_STORAGE_KEY]: 'stored' })
    expect(resolveAgentToken('cfg', { href: 'http://localhost:3210/', storage })).toBe('stored')
    expect(resolveAgentToken('cfg', { href: 'http://localhost:3210/', storage: memoryStorage() })).toBe('cfg')
    expect(resolveAgentToken('cfg')).toBe('cfg')
  })

  it('survives unavailable storage', () => {
    const broken = {
      getItem: () => {
        throw new Error('blocked')
      },
      setItem: () => {
        throw new Error('blocked')
      }
    }
    expect(resolveAgentToken('cfg', { href: 'http://x/?agentToken=q', storage: broken })).toBe('q')
    expect(resolveAgentToken('cfg', { href: 'http://x/', storage: broken })).toBe('cfg')
  })
})
