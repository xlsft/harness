import { describe, expect, it } from 'vitest'
import { MockSshApi, readActiveHost, writeActiveHost } from '../app/lib/ssh'

function memoryStorage() {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) }
}

describe('the window\'s SSH host', () => {
  it('survives a reload and goes away on leaving', () => {
    const s = memoryStorage()
    expect(readActiveHost(s)).toBeNull()
    writeActiveHost({ hostId: 'ssh_1', name: 'box' }, s)
    expect(readActiveHost(s)).toEqual({ hostId: 'ssh_1', name: 'box' })
    writeActiveHost(null, s)
    expect(readActiveHost(s)).toBeNull()
  })

  it('never takes a broken value for a host', () => {
    const s = memoryStorage()
    s.setItem('harness.sshHost', '{nope')
    expect(readActiveHost(s)).toBeNull()
    s.setItem('harness.sshHost', '{"name":"no id"}')
    expect(readActiveHost(s)).toBeNull()
  })
})

describe('MockSshApi', () => {
  it('keeps hosts and has nothing to connect to', async () => {
    const api = new MockSshApi()
    const h = await api.save({ name: '', destination: 'me@box', port: 0, identityFile: '' })
    expect((await api.list()).map(x => x.destination)).toEqual(['me@box'])
    expect((await api.checkKey()).status).toBe('unknown')
    await expect(api.connect()).rejects.toThrow(/no SSH hosts/)
    await api.remove(h.id)
    expect(await api.list()).toEqual([])
  })
})
