import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { PluginContentsSchema, PluginSchema } from '~/gen/harness/agent/v1/plugins_pb'
import { pluginFromProto } from '~/lib/plugins/connect'
import { MockPluginsApi } from '~/lib/plugins/mock'
import { PANE_PAGE } from '~/lib/plugins/mockPane'
import { capabilityLabel } from '~/lib/plugins/types'
import { FROM_PLUGIN, HOST_MARKER, pluginPaneDocument } from '~/lib/plugins/ui'

const catalog = (rel: string) => readFileSync(new URL(`../../../catalog/plugins/session-cost/${rel}`, import.meta.url), 'utf8')

describe('plugin UI', () => {
  it('puts the theme and the bridge where the client left room, after the CSP', () => {
    const html = `<!DOCTYPE html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'"/>${HOST_MARKER}<title>x</title></head><body></body></html>`
    const doc = pluginPaneDocument(html, 'nonce-1')
    expect(doc).not.toContain(HOST_MARKER)
    const csp = doc.indexOf('Content-Security-Policy')
    const style = doc.indexOf('<style>')
    const script = doc.indexOf('<script>')
    expect(csp).toBeGreaterThan(0)
    expect(style).toBeGreaterThan(csp)
    expect(script).toBeGreaterThan(style)
    expect(doc).toContain('"nonce-1"')
    expect(doc).toContain('W.harness=Object.freeze({version:1')
    // The bridge removes itself, so the page can't read the nonce from the DOM.
    expect(doc).toContain('if(s)s.remove()')
    expect(() => pluginPaneDocument('<p>no room</p>', 'n')).toThrow()
  })

  it('reads panes, status-bar items and grants from the client', () => {
    const vm = pluginFromProto(create(PluginSchema, {
      id: 'a@b', uiBlocked: 'off', uiGrants: [{ capability: 'session.read', granted: true, grantedAt: '2026-11-07T00:00:00Z' }],
      contents: create(PluginContentsSchema, { panes: [{ pluginId: 'a@b', id: 'p', title: 'P', page: './p.html' }], statusItems: [{ pluginId: 'a@b', id: 's', text: 'S', pane: 'p' }], uiCapabilities: ['session.read'], uiNetwork: ['https://x.example'] })
    }))
    expect(vm.contents.panes[0]!.title).toBe('P')
    expect(vm.contents.statusItems[0]!.pane).toBe('p')
    expect(vm.uiGrants[0]!.granted).toBe(true)
    expect(vm.uiBlocked).toBe('off')
    expect(capabilityLabel('network', vm.contents.uiNetwork)).toContain('https://x.example')
  })

  it('tells a plugin\'s message apart', () => {
    expect('[from plugin session-cost] Explain the cost'.match(FROM_PLUGIN)?.[1]).toBe('session-cost')
    expect('[from plugin] x'.match(FROM_PLUGIN)).toBeNull()
  })

  it('the demo store asks before a capability answers, and only for declared ones', async () => {
    const api = new MockPluginsApi()
    const list = await api.listUi()
    expect(list.panes.map(p => p.id)).toEqual(['meter'])
    expect(list.statusItems[0]!.pane).toBe('meter')
    const id = 'session-cost@harness-catalog'
    expect((await api.callUi(id, 's1', 'session.read', '{}')).needsApproval).toBe('session.read')
    await expect(api.callUi(id, 's1', 'session.prompt', '{}')).rejects.toThrow('does not declare')
    await expect(api.setUiGrant(id, 'session.read', true, 'sha256:other')).rejects.toThrow('changed')
    await api.setUiGrant(id, 'session.read', true, list.contentHashes[id]!)
    const r = await api.callUi(id, 's1', 'session.read', '{}')
    expect(JSON.parse(r.resultJson).costUsd).toBeGreaterThan(0)
    await api.setUiGrant(id, 'session.read', false, '')
    expect((await api.callUi(id, 's1', 'session.read', '{}')).needsApproval).toBe('session.read')
  })

  it('the demo pane is the catalog\'s example, inlined as the client inlines it', () => {
    expect(PANE_PAGE).toContain(`<style>${catalog('ui/meter.css')}</style>`)
    expect(PANE_PAGE).toContain(`<script>${catalog('ui/meter.js')}</script>`)
    const body = catalog('ui/meter.html')
    expect(PANE_PAGE).toContain(body.slice(body.indexOf('<main>'), body.indexOf('</main>')))
  })
})
