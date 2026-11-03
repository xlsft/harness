import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { DeviceSchema, User_Role, UserSchema, UserSource } from '~/gen/harness/auth/v1/auth_pb'
import { deviceView, profileView } from '~/lib/account/connect'
import { MockAccountApi } from '~/lib/account/mock'

describe('profileView / deviceView', () => {
  it('maps role, source, avatar and the current device', () => {
    const p = profileView(create(UserSchema, {
      id: 'usr_1',
      username: 'ada',
      email: 'ada@example.com',
      name: 'Ada',
      image: 'data:image/png;base64,AAAA',
      role: User_Role.ADMIN,
      source: UserSource.LDAP,
      createdAt: '2026-01-01T00:00:00Z',
      lastSignInAt: ''
    }))
    expect(p).toEqual({ id: 'usr_1', username: 'ada', email: 'ada@example.com', name: 'Ada', image: 'data:image/png;base64,AAAA', role: 'admin', source: 'ldap', createdAt: '2026-01-01T00:00:00Z', lastSignInAt: '' })
    expect(profileView(undefined)).toMatchObject({ role: 'user', source: 'local', image: '' })

    const d = deviceView(create(DeviceSchema, { id: 'dev_1', name: 'box', os: 'linux', arch: 'amd64', clientName: 'harness-client', createdAt: 'c', lastSeenAt: 's', current: true }))
    expect(d).toEqual({ id: 'dev_1', name: 'box', os: 'linux', arch: 'amd64', clientName: 'harness-client', createdAt: 'c', lastSeenAt: 's', current: true })
  })
})

describe('MockAccountApi', () => {
  it('updates the profile and follows the server rules', async () => {
    const a = new MockAccountApi()
    expect((await a.update({ name: ' Ada ', image: 'data:image/png;base64,AAAA' }))).toMatchObject({ name: 'Ada', image: 'data:image/png;base64,AAAA' })
    expect((await a.update({ image: '' })).image).toBe('')
    await expect(a.update({ image: 'https://example.com/a.png' })).rejects.toThrow()
    await expect(a.changePassword('wrong', 'new-password-1')).rejects.toThrow(/incorrect/)
    await a.changePassword('password-123', 'new-password-1', true)
    expect((await a.devices()).map(d => d.current)).toEqual([true])
    await expect(a.revokeDevice('dev_this')).rejects.toThrow()

    const ldap = new MockAccountApi('ldap')
    await expect(ldap.update({ name: 'Other' })).rejects.toThrow(/directory/)
    await expect(ldap.changePassword('password-123', 'new-password-1')).rejects.toThrow(/LDAP/)
  })
})
