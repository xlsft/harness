// In-memory RemoteApi for the mock transport. Like the server, it keeps the app's secrets write-only.

import type { ChatAppInput, ChatAppView, ChatIdentityView, FolderView, MachineView, RemoteApi, RemoteStartInput } from './types'

export class MockRemoteApi implements RemoteApi {
  started: RemoteStartInput[] = []
  private hosts: MachineView[] = [
    { hostId: 'hst_laptop', deviceId: 'dev_laptop', name: 'Laptop', online: true, remoteStarts: true, daemon: false, lastSeenAt: new Date().toISOString() },
    { hostId: 'hst_desk', deviceId: 'dev_desk', name: 'Desktop', online: false, remoteStarts: true, daemon: false, lastSeenAt: new Date(Date.now() - 3_600_000).toISOString() }
  ]

  private folderList: FolderView[] = [
    { path: '/Users/demo/src/harness', name: 'harness', branch: 'main' },
    { path: '/Users/demo/src/site', name: 'site', branch: 'dev' }
  ]

  private app: ChatAppView = { configured: false, enabled: false, teamId: '', teamName: '', botUserId: '', requestUrl: 'http://localhost:3000/api/chat/slack/events', matchByEmail: true, lastEventAt: '', lastError: '', disabledByPolicy: false }
  private ids: ChatIdentityView[] = []

  async machines() {
    return this.hosts
  }

  async folders(hostId: string) {
    const h = this.hosts.find(x => x.hostId === hostId)
    if (!h?.remoteStarts) throw new Error('This machine does not take remote starts')
    if (!h.online) throw new Error('The host is offline')
    return this.folderList
  }

  async start(input: RemoteStartInput) {
    if (!this.folderList.some(f => f.path === input.folder)) throw new Error(`${input.folder} is not one of this machine's recent folders`)
    this.started.push(input)
    return `mock-remote-${this.started.length}`
  }

  async chatApp() {
    return { ...this.app }
  }

  async putChatApp(input: ChatAppInput) {
    if (!this.app.configured && (!input.botToken || !input.signingSecret)) throw new Error('A bot token and a signing secret are required')
    this.app = { ...this.app, configured: true, enabled: input.enabled, matchByEmail: input.matchByEmail, teamId: 'T0DEMO', teamName: 'Demo', botUserId: 'UBOT' }
    return { ...this.app }
  }

  async deleteChatApp() {
    this.app = { ...this.app, configured: false, enabled: false, teamId: '', teamName: '', botUserId: '' }
  }

  async identities() {
    return { identities: [...this.ids], available: this.app.configured && this.app.enabled }
  }

  async startLink() {
    return { code: 'ABCD-2345', expiresAt: new Date(Date.now() + 15 * 60_000).toISOString() }
  }

  async unlink(id: ChatIdentityView) {
    this.ids = this.ids.filter(x => x.externalUserId !== id.externalUserId || x.teamId !== id.teamId)
  }
}
