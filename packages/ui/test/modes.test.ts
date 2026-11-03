import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { createSessionState, sessionMeta } from '~/lib/session/reducer'
import { MockTransport } from '~/lib/transport/mock'
import { MockUsersApi } from '~/lib/users/mock'
import { useSessionStore } from '~/stores/sessions'
import { allowedModes, fitMode, nextMode } from '~/utils/composer'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v }))

describe('mode helpers', () => {
  it('hide disabled modes and fall back to the first allowed one', () => {
    expect(allowedModes(['bypass', 'auto']).map(m => m.key)).toEqual(['default', 'accept_edits', 'plan'])
    expect(fitMode('auto', ['auto'])).toBe('default')
    expect(fitMode('plan', ['auto'])).toBe('plan')
  })

  it('cycle in the client order and skip disabled modes', () => {
    const cycle = ['default', 'accept_edits', 'plan', 'auto'] as const
    expect(nextMode('default', cycle, ['accept_edits'])).toBe('plan')
    expect(nextMode('plan', cycle, ['auto'])).toBe('default')
    expect(nextMode('bypass', cycle, [])).toBe('default')
    // An older client reports no cycle: the built-in one.
    expect(nextMode('default', [], [])).toBe('accept_edits')
  })
})

describe('MockUsersApi mode policy', () => {
  it('never disables Ask and is admin only', async () => {
    const api = new MockUsersApi()
    await expect(api.setModePolicy(['default'])).rejects.toThrow(/cannot be disabled/)
    expect(await api.setModePolicy(['bypass', 'auto'])).toEqual(['auto', 'bypass'])
    await expect(new MockUsersApi(false).getModePolicy()).rejects.toThrow(/Admin/)
  })
})

describe('store', () => {
  it('reads the policy from Initialize and rolls a refused mode change back', async () => {
    setActivePinia(createPinia())
    const store = useSessionStore()
    const transport = new MockTransport({ tick: 0 })
    store.transport = transport
    const [info] = await transport.listSessions()
    store.sessions[info!.id] = createSessionState(info!.id, sessionMeta(info!))
    const before = store.sessions[info!.id]!.meta.permissionMode

    await transport.users().setModePolicy(['bypass', 'auto'])
    await store.refreshModes()
    expect(store.disabledModes).toEqual(['auto', 'bypass'])
    expect(store.permissionModes).toEqual(['default', 'accept_edits', 'plan'])
    expect(store.availableModes.map(m => m.key)).toEqual(['default', 'accept_edits', 'plan'])

    await store.setPermissionMode(info!.id, 'bypass')
    expect(store.sessions[info!.id]!.meta.permissionMode).toBe(before)
    expect(store.error).toMatch(/disabled by your administrator/)
  })
})
