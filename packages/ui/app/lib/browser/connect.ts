// BrowserApi over AgentService's browser RPCs, on the same connect transport (and agent token) as the rest of
// AgentService; the live view goes over the client's browser WebSocket (./connection.ts).

import { create } from '@bufbuild/protobuf'
import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { BrowserViewportSchema } from '~/gen/harness/agent/v1/browser_pb'
import { BrowserConnection, webSocketOpener, type BrowserLink } from './connection'
import { availabilityView, presetPb, previewServerView, stateView, type BrowserApi, type BrowserAvailabilityVM, type BrowserInstallHandle, type OpenBrowserOptions } from './types'

export class ConnectBrowserApi implements BrowserApi {
  private client: Client<typeof AgentService>
  private openSocket: BrowserLink['socket']

  // agentUrl is the client's base URL (the socket is served next to AgentService); WS is for tests.
  constructor(transport: Transport, agentUrl: string, WS?: typeof WebSocket) {
    this.client = createClient(AgentService, transport)
    this.openSocket = webSocketOpener(agentUrl, WS)
  }

  async availability() {
    return availabilityView((await this.client.getBrowserAvailability({})).availability)
  }

  install(onProgress?: (downloaded: number, total: number) => void): BrowserInstallHandle {
    const abort = new AbortController()
    const done = (async () => {
      let out: BrowserAvailabilityVM | undefined
      for await (const r of this.client.installBrowser({}, { signal: abort.signal })) {
        onProgress?.(Number(r.downloadedBytes), Number(r.totalBytes))
        if (r.done) out = availabilityView(r.availability)
      }
      return out
    })()
    return { done, cancel: () => abort.abort() }
  }

  open(sessionId: string, opts: OpenBrowserOptions = {}) {
    const link: BrowserLink = {
      open: async (id, first) => {
        const v = first ? opts.viewport : undefined
        const res = await this.client.openBrowser({
          sessionId: id,
          url: first ? opts.url ?? '' : '',
          viewport: v ? create(BrowserViewportSchema, { preset: presetPb(v.preset), width: v.width ?? 0, height: v.height ?? 0 }) : undefined
        })
        return { state: stateView(res.state), ticket: res.ticket }
      },
      socket: this.openSocket
    }
    const conn = new BrowserConnection(link, sessionId, opts.connection)
    void conn.connect()
    return conn
  }

  async listTabs(sessionId: string) {
    return stateView((await this.client.listBrowserTabs({ sessionId })).state)
  }

  async close(sessionId: string) {
    await this.client.closeBrowser({ sessionId })
  }

  async clearData(sessionId: string) {
    await this.client.clearBrowserData({ sessionId })
  }

  async previewServers(sessionId: string) {
    return (await this.client.listPreviewServers({ sessionId })).servers.map(previewServerView)
  }

  async stopPreview(sessionId: string, serverId: string) {
    await this.client.stopPreviewServer({ sessionId, serverId })
  }

  async previewLogs(sessionId: string, serverId: string, opts: { errorsOnly?: boolean } = {}) {
    return (await this.client.getPreviewLogs({ sessionId, serverId, errorsOnly: !!opts.errorsOnly })).lines
  }
}
