// SshApi: saved SSH hosts and connections to the client on them (proto/harness/agent/v1/ssh_hosts.proto). All of it runs in
// the local client: it holds the addresses, checks host keys, deploys and tunnels. Once connected, this window talks to the
// host's client through the tunnel as it would to a local one, so every screen just works; the window reloads to switch.
// Device-local: nothing here is synced. Keys never pass through: ssh uses the user's own agent and config.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { SshHostKeyStatus, SshHostService, type SshConnection } from '~/gen/harness/agent/v1/ssh_hosts_pb'

export interface SshHostVM {
  id: string
  name: string
  destination: string
  port: number
  identityFile: string
}

export type SshKeyStatus = 'verified' | 'unknown' | 'changed' | 'unreachable'

export interface SshKeyCheck {
  status: SshKeyStatus
  // Unknown: what to show, one line per key, and what to trust it with.
  fingerprints: string[]
  fingerprint: string
  // Unreachable: ssh's words.
  error: string
}

export interface SshStage {
  stage: string
  message: string
  // Sign-in: where to confirm the code.
  verificationUri: string
  userCode: string
}

export interface SshConnectionVM {
  hostId: string
  url: string
  token: string
  platform: string
  email: string
}

export interface SshApi {
  list(): Promise<SshHostVM[]>
  save(host: Omit<SshHostVM, 'id'> & { id?: string }): Promise<SshHostVM>
  remove(id: string): Promise<void>
  checkKey(id: string): Promise<SshKeyCheck>
  trustKey(id: string, fingerprint: string): Promise<void>
  forgetKey(id: string): Promise<void>
  // Connects, reporting each stage. Stopping `signal` stops it.
  connect(id: string, origin: string, onStage: (s: SshStage) => void, signal?: AbortSignal): Promise<SshConnectionVM>
  disconnect(id: string): Promise<void>
  connections(): Promise<SshConnectionVM[]>
}

const hostView = (h: { id: string, name: string, destination: string, port: number, identityFile: string }): SshHostVM =>
  ({ id: h.id, name: h.name, destination: h.destination, port: h.port, identityFile: h.identityFile })

const connectionView = (c: SshConnection): SshConnectionVM =>
  ({ hostId: c.hostId, url: c.url, token: c.token, platform: c.platform, email: c.email })

const KEY_STATUS: Partial<Record<SshHostKeyStatus, SshKeyStatus>> = {
  [SshHostKeyStatus.VERIFIED]: 'verified',
  [SshHostKeyStatus.UNKNOWN]: 'unknown',
  [SshHostKeyStatus.CHANGED]: 'changed'
}

export class ConnectSshApi implements SshApi {
  private client: Client<typeof SshHostService>

  constructor(transport: Transport) {
    this.client = createClient(SshHostService, transport)
  }

  async list() {
    return (await this.client.listSshHosts({})).hosts.map(hostView)
  }

  async save(host: Omit<SshHostVM, 'id'> & { id?: string }) {
    const r = await this.client.saveSshHost({ host: { id: host.id ?? '', name: host.name, destination: host.destination, port: host.port, identityFile: host.identityFile } })
    return hostView(r.host!)
  }

  async remove(id: string) {
    await this.client.deleteSshHost({ id })
  }

  async checkKey(id: string) {
    const r = await this.client.checkSshHostKey({ id })
    return { status: KEY_STATUS[r.status] ?? 'unreachable', fingerprints: r.fingerprints, fingerprint: r.fingerprint, error: r.error }
  }

  async trustKey(id: string, fingerprint: string) {
    await this.client.trustSshHostKey({ id, fingerprint })
  }

  async forgetKey(id: string) {
    await this.client.forgetSshHostKey({ id })
  }

  async connect(id: string, origin: string, onStage: (s: SshStage) => void, signal?: AbortSignal) {
    let connection: SshConnectionVM | undefined
    for await (const r of this.client.connectSshHost({ id, origin }, { signal })) {
      onStage({ stage: r.stage, message: r.message, verificationUri: r.verificationUri, userCode: r.userCode })
      if (r.connection) connection = connectionView(r.connection)
    }
    if (!connection) throw new Error('The connection ended before it was ready')
    return connection
  }

  async disconnect(id: string) {
    await this.client.disconnectSshHost({ id })
  }

  async connections() {
    return (await this.client.listSshConnections({})).connections.map(connectionView)
  }
}

// In memory, for the mock transport: hosts can be listed and saved, but there is nothing to connect to.
export class MockSshApi implements SshApi {
  private hosts: SshHostVM[] = []

  async list() {
    return [...this.hosts]
  }

  async save(host: Omit<SshHostVM, 'id'> & { id?: string }) {
    const h = { ...host, id: host.id || `ssh_${this.hosts.length + 1}` }
    this.hosts = [...this.hosts.filter(x => x.id !== h.id), h]
    return h
  }

  async remove(id: string) {
    this.hosts = this.hosts.filter(x => x.id !== id)
  }

  async checkKey(): Promise<SshKeyCheck> {
    return { status: 'unknown', fingerprints: ['SHA256:mock-fingerprint (ED25519)'], fingerprint: 'SHA256:mock-fingerprint (ED25519)', error: '' }
  }

  async trustKey() {}
  async forgetKey() {}

  async connect(): Promise<SshConnectionVM> {
    throw new Error('The demo has no SSH hosts to connect to')
  }

  async disconnect() {}

  async connections() {
    return [] as SshConnectionVM[]
  }
}

const STORAGE_KEY = 'harness.sshHost'

export interface ActiveSshHost {
  hostId: string
  name: string
}

// Which host this window is on: it survives a reload of the window, and the connection itself lives in the local client.
export function readActiveHost(storage: Pick<Storage, 'getItem'> | undefined = globalThis.sessionStorage): ActiveSshHost | null {
  try {
    const v = JSON.parse(storage?.getItem(STORAGE_KEY) ?? 'null')
    return v && typeof v.hostId === 'string' ? { hostId: v.hostId, name: String(v.name ?? '') } : null
  } catch {
    return null
  }
}

export function writeActiveHost(host: ActiveSshHost | null, storage: Pick<Storage, 'setItem' | 'removeItem'> | undefined = globalThis.sessionStorage) {
  if (host) storage?.setItem(STORAGE_KEY, JSON.stringify(host))
  else storage?.removeItem(STORAGE_KEY)
}
