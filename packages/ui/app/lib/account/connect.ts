// AccountApi over harness.auth.v1.DeviceAuthService, on the same connect transport (and agent token) as
// AgentService: the client forwards these methods to the server (packages/client/server/coreproxy.go).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { DeviceAuthService, User_Role, UserSource, type Device, type User } from '~/gen/harness/auth/v1/auth_pb'
import type { AccountApi, AccountDevice, AccountPatch, AccountProfile } from './types'

export function profileView(u: User | undefined): AccountProfile {
  return {
    id: u?.id ?? '',
    username: u?.username ?? '',
    email: u?.email ?? '',
    name: u?.name ?? '',
    image: u?.image ?? '',
    imageObject: u?.imageObject ? { sha256: u.imageObject.sha256, size: Number(u.imageObject.size), mediaType: u.imageObject.mediaType } : undefined,
    role: u?.role === User_Role.ADMIN ? 'admin' : u?.role === User_Role.AUDITOR ? 'auditor' : u?.role === User_Role.BILLING ? 'billing' : 'user',
    source: u?.source === UserSource.LDAP ? 'ldap' : u?.source === UserSource.SSO ? 'sso' : u?.source === UserSource.SCIM ? 'scim' : 'local',
    createdAt: u?.createdAt ?? '',
    lastSignInAt: u?.lastSignInAt ?? ''
  }
}

export function deviceView(d: Device): AccountDevice {
  return {
    id: d.id,
    name: d.name,
    os: d.os,
    arch: d.arch,
    clientName: d.clientName,
    createdAt: d.createdAt,
    lastSeenAt: d.lastSeenAt,
    current: d.current
  }
}

export class ConnectAccountApi implements AccountApi {
  private client: Client<typeof DeviceAuthService>

  constructor(transport: Transport) {
    this.client = createClient(DeviceAuthService, transport)
  }

  async me() {
    return profileView((await this.client.getMe({})).user)
  }

  async update(patch: AccountPatch) {
    return profileView((await this.client.updateMe({ name: patch.name, image: patch.image })).user)
  }

  async changePassword(currentPassword: string, newPassword: string, revokeOtherSessions = false) {
    await this.client.changePassword({ currentPassword, newPassword, revokeOtherSessions })
  }

  async devices() {
    return (await this.client.listDevices({})).devices.map(deviceView)
  }

  async revokeDevice(deviceId: string) {
    await this.client.revokeDevice({ deviceId })
  }
}
