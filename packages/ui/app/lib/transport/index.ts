import { ConnectTransport } from './connect'
import { MockTransport } from './mock'
import type { SessionTransport } from './types'

export type { SessionTransport, ModelInfo, SubscribeHandlers } from './types'
export { MockTransport } from './mock'
export { ConnectTransport } from './connect'
export { browserAgentToken, resolveAgentToken } from './token'
export type { LocalModel, LocalModelsApi, LocalProvider, LocalProviderKindName, LocalProvidersState, AddLocalProviderInput, LocalProviderPatch, PullProgress, PullHandle, PullHandlers } from './localModels'
export { isLocalModelId } from './localModels'

// The mock is the default; set NUXT_PUBLIC_TRANSPORT=connect and NUXT_PUBLIC_AGENT_URL (plus the client's
// token) to use a real client.
export function createTransport(kind: string, agentUrl: string, agentToken = ''): SessionTransport {
  if (kind === 'connect' && agentUrl) return new ConnectTransport(agentUrl, agentToken)
  return new MockTransport()
}
