import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { PluginSchema, PluginContentsSchema, PluginStatus } from '~/gen/harness/agent/v1/plugins_pb'
import { pluginFromProto } from '~/lib/plugins/connect'
import { MockPluginsApi } from '~/lib/plugins/mock'
import { runsPrograms, statusLabel } from '~/lib/plugins/types'

describe('plugins', () => {
  it('reads a plugin from the client', () => {
    const vm = pluginFromProto(create(PluginSchema, { id: 'a@b', status: PluginStatus.UNTRUSTED_FOLDER, contentHash: 'sha256:x', contents: create(PluginContentsSchema, { skills: ['a:s'], hooks: [{ event: 'Stop', command: 'x' }] }) }))
    expect(vm.status).toBe('untrusted_folder')
    expect(statusLabel(vm.status)).toBe('Folder not trusted')
    expect(vm.contents.skills).toEqual(['a:s'])
    expect(runsPrograms(vm.contents)).toBe(true)
  })

  it('reads an unknown status as something that does not run', () => {
    expect(pluginFromProto(create(PluginSchema, { id: 'a@b' })).status).toBe('broken')
  })

  it('tells text from programs', () => {
    const vm = pluginFromProto(create(PluginSchema, { id: 'a@b', contents: create(PluginContentsSchema, { skills: ['a:s'] }) }))
    expect(runsPrograms(vm.contents)).toBe(false)
  })

  it('the demo store keeps approval tied to what was shown', async () => {
    const api = new MockPluginsApi()
    const [p] = (await api.list()).plugins
    await expect(api.setApproval(p!.id, true, 'sha256:other')).rejects.toThrow('changed')
    expect((await api.list()).plugins[0]!.status).toBe('needs_approval')
    await api.setApproval(p!.id, true, p!.contentHash)
    expect((await api.list()).plugins[0]!.status).toBe('active')
    await api.setEnabled(p!.id, false)
    expect((await api.list()).plugins[0]!.status).toBe('disabled')
  })

  it('the demo store installs from a marketplace and marks the listing', async () => {
    const api = new MockPluginsApi()
    await api.install('release-notes@acme')
    const { marketplaces, plugins } = await api.list()
    expect(plugins.map(x => x.id)).toContain('release-notes@acme')
    expect(marketplaces.find(m => m.name === 'acme')!.plugins.find(l => l.id === 'release-notes@acme')!.installed).toBe(true)
  })
})
