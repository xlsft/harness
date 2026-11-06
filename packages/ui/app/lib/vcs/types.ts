// VcsApi: what Settings > Code hosts needs. The local CLIs come from AgentService; connections and the user's own
// authorization from harness.vcs.v1.VcsService, which the client forwards to the server with the user's credentials.
// The server keeps connections admin-only and their secrets write-only. Two implementations: connect.ts and mock.ts.

export type CodeHostKind = 'github' | 'gitlab'

export type UserAuthState = 'not_connected' | 'pending' | 'connected' | 'error'

export interface CliHostView {
  host: string
  login: string
}

export interface CliView {
  name: string
  installed: boolean
  version: string
  hosts: CliHostView[]
  loginCommand: string
  problem: string
}

export interface UserConnectionView {
  id: string
  kind: CodeHostKind
  name: string
  baseUrl: string
  host: string
  installUrl: string
  state: UserAuthState
  login: string
  canWrite: boolean
}

export interface ConnectionView {
  id: string
  kind: CodeHostKind
  name: string
  baseUrl: string
  healthy: boolean
  lastError: string
  installUrl: string
  webhooksOk: boolean
}

export interface AuthStart {
  authorizationUrl: string
  flowId: string
}

export interface AuthStatus {
  state: UserAuthState
  login: string
  error: string
}

export interface GitHubManifest {
  // The admin's browser posts `manifest` as a form to postUrl.
  postUrl: string
  manifest: string
}

export interface GitLabConnectionInput {
  name: string
  baseUrl: string
  clientId?: string
  clientSecret?: string
  webhookSecret?: string
  accessToken?: string
  allowPrivateNetwork: boolean
}

export interface VcsApi {
  clis(): Promise<CliView[]>
  userConnections(): Promise<UserConnectionView[]>
  startAuth(connectionId: string): Promise<AuthStart>
  authStatus(connectionId: string, flowId: string): Promise<AuthStatus>
  revokeAuth(connectionId: string): Promise<void>
  // Admins only (the server refuses everyone else).
  connections(): Promise<ConnectionView[]>
  gitHubManifest(input: { baseUrl: string, organization: string, appName: string }): Promise<GitHubManifest>
  putGitLab(input: GitLabConnectionInput): Promise<ConnectionView>
  deleteConnection(id: string): Promise<void>
}
