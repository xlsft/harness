import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { DoctorStatus, RunDoctorResponseSchema } from '~/gen/harness/agent/v1/doctor_pb'
import { UserPreferencesSchema } from '~/gen/harness/agent/v1/agent_pb'
import { ManagedPolicyViewSchema } from '~/gen/harness/agent/v1/managed_policy_pb'
import { deepLinkRoute, linkDraft } from '~/lib/deeplink'
import { doctorReport } from '~/lib/doctor'
import { policyView } from '~/lib/enterprise/connect'
import { MockTransport } from '~/lib/transport/mock'

describe('harness:// links', () => {
  it('go where the shell said, and only a new-session link leaves a draft', () => {
    expect(deepLinkRoute({ kind: 'session', id: 'abc' })).toEqual({ path: '/', query: { session: 'abc' } })
    expect(deepLinkRoute({ kind: 'settings', section: 'about' })).toEqual({ path: '/', query: { settings: 'about' } })
    expect(deepLinkRoute({ kind: 'new', prompt: 'p', folder: '/code', folder_dropped: false })).toEqual({ path: '/', query: { new: '1', cwd: '/code' } })
    expect(deepLinkRoute({ kind: 'new', prompt: 'p', folder: '', folder_dropped: true })).toEqual({ path: '/', query: { new: '1' } })
    expect(linkDraft({ kind: 'new', prompt: 'fix it', folder: '/code', folder_dropped: false })).toEqual({ prompt: 'fix it', folder: '/code', folderDropped: false })
    expect(linkDraft({ kind: 'session', id: 'abc' })).toBeNull()
  })

  it('never put a prompt in the address', () => {
    const route = deepLinkRoute({ kind: 'new', prompt: 'a secret plan', folder: '/code', folder_dropped: false })
    expect(JSON.stringify(route)).not.toContain('secret')
  })
})

describe('doctor', () => {
  it('maps the client\'s report', () => {
    const r = doctorReport(create(RunDoctorResponseSchema, {
      status: DoctorStatus.WARN,
      clientVersion: '0.3.0',
      checks: [
        { id: 'signin', title: 'Sign-in', status: DoctorStatus.PASS, detail: 'Signed in', hint: '' },
        { id: 'mcp', title: 'MCP servers', status: DoctorStatus.WARN, detail: '1 failed', hint: 'look' },
        { id: 'server', title: 'Server', status: DoctorStatus.UNSPECIFIED, detail: '', hint: '' }
      ]
    }))
    expect(r.status).toBe('warn')
    expect(r.checks.map(c => c.status)).toEqual(['pass', 'warn', 'skip'])
    expect(r.checks[1]).toMatchObject({ id: 'mcp', hint: 'look' })
  })

  it('the mock answers with and without MCP', async () => {
    const t = new MockTransport()
    expect((await t.runDoctor({ skipMcp: true })).checks.find(c => c.id === 'mcp')?.status).toBe('skip')
    expect((await t.runDoctor()).status).toBe('warn')
  })
})

describe('client version bounds', () => {
  it('come through the admin form and the user\'s view', () => {
    const view = policyView({ minClientVersion: '0.2.0', maxClientVersion: '0.4.1' } as Parameters<typeof policyView>[0])
    expect(view).toMatchObject({ minClientVersion: '0.2.0', maxClientVersion: '0.4.1' })
    expect(policyView(undefined)).toMatchObject({ minClientVersion: '', maxClientVersion: '' })
    const prefs = create(UserPreferencesSchema, { managedPolicy: create(ManagedPolicyViewSchema, { active: true, maxClientVersion: '0.4.1' }) })
    expect(prefs.managedPolicy?.maxClientVersion).toBe('0.4.1')
  })
})
