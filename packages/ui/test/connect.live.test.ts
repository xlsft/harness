// Live check of ConnectTransport against a running `harness-client serve --listen` (connect-web over fetch,
// HTTP/1.1). Skipped unless HARNESS_AGENT_URL and HARNESS_AGENT_TOKEN are set; scripts/e2e/ui-connect.sh
// starts a client with the fake gateway and runs it.
import { ConnectError, Code, createClient, type Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { describe, expect, it } from 'vitest'
import { AgentService, SendUserMessageRequest_Delivery, type SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { bearer, ConnectTransport } from '~/lib/transport/connect'

const url = process.env.HARNESS_AGENT_URL ?? ''
const token = process.env.HARNESS_AGENT_TOKEN ?? ''
const cwd = process.env.HARNESS_AGENT_CWD ?? process.cwd()
const origin = process.env.HARNESS_AGENT_ORIGIN ?? 'http://localhost:3210'

// Browsers add Origin themselves; Node's fetch does not, so set it to exercise the CORS path.
const withOrigin = (o: string): Interceptor => next => (req) => {
  req.header.set('Origin', o)
  return next(req)
}

describe.skipIf(!url || !token)('ConnectTransport against a live client', () => {
  it('rejects a missing token and a foreign origin', async () => {
    const anon = createClient(AgentService, createConnectTransport({ baseUrl: url, interceptors: [withOrigin(origin)] }))
    await expect(anon.listSessions({})).rejects.toSatisfy(e => ConnectError.from(e).code === Code.Unauthenticated)
    const foreign = createClient(AgentService, createConnectTransport({
      baseUrl: url,
      interceptors: [bearer(token), withOrigin('https://evil.example')]
    }))
    await expect(foreign.listSessions({})).rejects.toBeDefined()
  })

  it('initializes, creates a session and streams a turn', async () => {
    const t = new ConnectTransport(url, token)
    const init = await t.initialize()
    expect(init.modelIds.length).toBeGreaterThan(0)

    // ConnectTransport has no createSession yet; use a raw client with the same interceptor.
    const raw = createClient(AgentService, createConnectTransport({ baseUrl: url, interceptors: [bearer(token), withOrigin(origin)] }))
    const { session } = await raw.createSession({ cwd })
    const sid = session!.id

    const events: SessionEvent[] = []
    const finished = new Promise<void>((resolve, reject) => {
      const stop = t.subscribe(sid, 0, {
        onEvent: (ev) => {
          events.push(ev)
          if (ev.event.case === 'turnFinished') {
            stop()
            resolve()
          }
        },
        onError: reject
      })
    })
    await t.sendUserMessage(sid, 'u1', 'hello', SendUserMessageRequest_Delivery.QUEUE)
    await finished
    expect(events.some(e => e.event.case === 'assistantMessage')).toBe(true)
    expect((await t.listSessions()).some(s => s.id === sid)).toBe(true)
  }, 30_000)
})
