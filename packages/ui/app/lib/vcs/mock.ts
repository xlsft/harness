// In-memory VcsApi for the mock transport. Like the server, it keeps connections to admins and secrets write-only.

import type { AuthStatus, ConnectionView, CliView, GitLabConnectionInput, UserConnectionView, VcsApi } from './types'

export class MockVcsApi implements VcsApi {
  admin = true
  private cliList: CliView[] = [
    { name: 'gh', installed: true, version: 'gh version 2.80.0', hosts: [{ host: 'github.com', login: 'demo' }], loginCommand: 'gh auth login', problem: '' },
    { name: 'glab', installed: false, version: '', hosts: [], loginCommand: 'glab auth login', problem: '' }
  ]

  private conns: ConnectionView[] = [
    { id: 'vcs_github', kind: 'github', name: 'GitHub', baseUrl: 'https://github.com', healthy: true, lastError: '', installUrl: 'https://github.com/apps/demo/installations/new', webhooksOk: true }
  ]

  private accounts = new Map<string, { state: AuthStatus['state'], login: string }>()
  private seq = 0

  async clis() {
    return this.cliList
  }

  async userConnections(): Promise<UserConnectionView[]> {
    return this.conns.map((c) => {
      const a = this.accounts.get(c.id)
      return { id: c.id, kind: c.kind, name: c.name, baseUrl: c.baseUrl, host: new URL(c.baseUrl).host, installUrl: c.installUrl, state: a?.state ?? 'not_connected', login: a?.login ?? '', canWrite: a?.state === 'connected' }
    })
  }

  async startAuth(connectionId: string) {
    this.requireConnection(connectionId)
    // The demo's "browser" finishes at once.
    this.accounts.set(connectionId, { state: 'connected', login: 'demo' })
    return { authorizationUrl: 'https://example.invalid/authorize', flowId: `flow-${++this.seq}` }
  }

  async authStatus(connectionId: string, _flowId: string): Promise<AuthStatus> {
    const a = this.accounts.get(connectionId)
    return { state: a?.state ?? 'not_connected', login: a?.login ?? '', error: '' }
  }

  async revokeAuth(connectionId: string) {
    this.accounts.delete(connectionId)
  }

  async connections() {
    this.requireAdmin()
    return this.conns
  }

  async gitHubManifest(input: { baseUrl: string, organization: string, appName: string }) {
    this.requireAdmin()
    return { postUrl: `${input.baseUrl || 'https://github.com'}/settings/apps/new?state=demo`, manifest: JSON.stringify({ name: input.appName || 'harness' }) }
  }

  async putGitLab(input: GitLabConnectionInput) {
    this.requireAdmin()
    if (!input.baseUrl) throw new Error('[invalid_argument] base_url is required')
    const c: ConnectionView = { id: `vcs_${++this.seq}`, kind: 'gitlab', name: input.name || 'GitLab', baseUrl: input.baseUrl, healthy: true, lastError: '', installUrl: '', webhooksOk: false }
    this.conns = [...this.conns, c]
    return c
  }

  async deleteConnection(id: string) {
    this.requireAdmin()
    this.conns = this.conns.filter(c => c.id !== id)
    this.accounts.delete(id)
  }

  private requireAdmin() {
    if (!this.admin) throw new Error('[permission_denied] Only admins can manage code host connections')
  }

  private requireConnection(id: string) {
    if (!this.conns.some(c => c.id === id)) throw new Error(`[not_found] Connection ${id} not found`)
  }
}
