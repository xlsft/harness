import { create } from '@bufbuild/protobuf'
import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { SessionInfoSchema } from '~/gen/harness/agent/v1/agent_pb'
import { createSessionState, sessionMeta } from '~/lib/session/reducer'
import { MockTransport } from '~/lib/transport/mock'
import { useSessionStore } from '~/stores/sessions'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v }))

describe('session relay', () => {
  it('maps the relay fields of SessionInfo', () => {
    const meta = sessionMeta(create(SessionInfoSchema, { id: 's', relayEnabled: false, relayAvailable: false, relayUnavailableReason: 'Not signed in to a harness server' }))
    expect(meta).toMatchObject({ relayEnabled: false, relayAvailable: false, relayReason: 'Not signed in to a harness server' })
    expect(createSessionState('x').meta).toMatchObject({ relayEnabled: false, relayAvailable: false, relayReason: '' })
  })

  async function setup() {
    setActivePinia(createPinia())
    const store = useSessionStore()
    const transport = new MockTransport({ tick: 0 })
    store.transport = transport
    const info = (await transport.listSessions()).find(i => !i.remote)!
    store.sessions[info.id] = createSessionState(info.id, sessionMeta(info))
    return { store, transport, id: info.id }
  }

  it('turns the switch off and on through the transport', async () => {
    const { store, transport, id } = await setup()
    expect(store.sessions[id]!.meta.relayEnabled).toBe(true)
    const call = vi.spyOn(transport, 'setSessionRelay')
    const p = store.setSessionRelay(id, false)
    // Optimistic: shown before the client answers.
    expect(store.sessions[id]!.meta.relayEnabled).toBe(false)
    await p
    expect(call).toHaveBeenCalledWith(id, false)
    expect(store.sessions[id]!.meta.relayEnabled).toBe(false)
    await store.setSessionRelay(id, true)
    expect(store.sessions[id]!.meta.relayEnabled).toBe(true)
  })

  it('rolls a refused change back and rethrows', async () => {
    const { store, transport, id } = await setup()
    await store.setSessionRelay(id, false)
    vi.spyOn(transport, 'setSessionRelay').mockRejectedValueOnce(new Error('[failed_precondition] Not signed in to a harness server'))
    await expect(store.setSessionRelay(id, true)).rejects.toThrow(/Not signed in/)
    expect(store.sessions[id]!.meta.relayEnabled).toBe(false)
  })

  it('applies the availability the client reports', async () => {
    const { store, transport, id } = await setup()
    vi.spyOn(transport, 'setSessionRelay').mockResolvedValueOnce(create(SessionInfoSchema, { id, relayEnabled: false, relayAvailable: false, relayUnavailableReason: 'Remote control is turned off by policy settings' }))
    await store.setSessionRelay(id, false)
    expect(store.sessions[id]!.meta).toMatchObject({ relayEnabled: false, relayAvailable: false, relayReason: 'Remote control is turned off by policy settings' })
  })

  it('refuses remote sessions in the mock', async () => {
    const transport = new MockTransport({ tick: 0 })
    const remote = (await transport.listSessions()).find(i => i.remote)!
    await expect(transport.setSessionRelay(remote.id, false)).rejects.toThrow(/own machine/)
  })
})
