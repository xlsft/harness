import { ConnectTransport } from './connect'
import { MockTransport } from './mock'
import type { SessionTransport } from './types'

export type { SessionTransport, ModelInfo, SubscribeHandlers } from './types'
export { MockTransport } from './mock'
export { ConnectTransport } from './connect'

// The mock is the default; set NUXT_PUBLIC_TRANSPORT=connect and NUXT_PUBLIC_AGENT_URL to use a real client.
export function createTransport(kind: string, agentUrl: string): SessionTransport {
  if (kind === 'connect' && agentUrl) return new ConnectTransport(agentUrl)
  return new MockTransport()
}
