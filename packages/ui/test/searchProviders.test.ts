import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ProviderPresetSchema, ProviderSchema, ProviderScope, SearchSettingsSchema } from '~/gen/harness/llm/v1/providers_pb'
import { presetView, providerView, searchSettingsView } from '~/lib/providers/connect'
import { MockProvidersApi } from '~/lib/providers/mock'

describe('search views', () => {
  it('maps a search preset with its options and the built-in flag', () => {
    const v = presetView(create(ProviderPresetSchema, {
      id: 'brave',
      group: 'search',
      displayName: 'Brave Search',
      type: 'brave',
      fields: [{ name: 'api_key', label: 'API key', required: true }],
      icon: 'brave',
      options: [
        { name: 'max_results', label: 'Results', kind: 'number', defaultValue: '10', min: 1, max: 20 },
        { name: 'safe_search', label: 'Safe search', kind: 'select', choices: [{ value: 'off', label: 'Off' }], defaultValue: 'moderate' },
        { name: 'country', label: 'Country', kind: 'odd' }
      ]
    }))
    expect(v).toMatchObject({ group: 'search', builtin: false })
    expect(v.options!.map(o => o.kind)).toEqual(['number', 'select', 'text'])
    expect(v.options![0]).toMatchObject({ min: 1, max: 20, defaultValue: '10' })
    expect(presetView(create(ProviderPresetSchema, { id: 'searxng', group: 'search', displayName: 'SearXNG', builtin: true })).builtin).toBe(true)
  })

  it('maps a search provider and its options', () => {
    const v = providerView(create(ProviderSchema, { id: 'u-x', scope: ProviderScope.PERSONAL, group: 'search', type: 'tavily', options: { max_results: '5' } }))
    expect(v).toMatchObject({ group: 'search', options: { max_results: '5' } })
  })

  it('maps the search settings, falling back to the built-in', () => {
    expect(searchSettingsView(undefined)).toEqual({ defaultProviderId: 'searxng', myProviderId: '', effectiveProviderId: 'searxng', editable: false })
    expect(searchSettingsView(create(SearchSettingsSchema, { defaultProviderId: 'brave', myProviderId: 'u-1', effectiveProviderId: 'u-1', editable: true }))).toEqual({
      defaultProviderId: 'brave',
      myProviderId: 'u-1',
      effectiveProviderId: 'u-1',
      editable: true
    })
  })
})

describe('MockProvidersApi search', () => {
  it('uses the built-in until an admin picks a default; everyone may pick their own', async () => {
    const admin = new MockProvidersApi(true)
    expect(await admin.getSearch()).toMatchObject({ defaultProviderId: 'searxng', effectiveProviderId: 'searxng', editable: true })
    await expect(admin.create({ scope: 'global', presetId: 'searxng' })).rejects.toThrow(/built in/)
    const g = await admin.create({ scope: 'global', presetId: 'brave', apiKey: 'sk-fake-search-123456', options: { country: 'de' } })
    expect(g).toMatchObject({ group: 'search', keyHint: '…3456', options: { country: 'DE' } })
    expect((await admin.setSearchDefault(g.id)).effectiveProviderId).toBe(g.id)
    await admin.update(g.id, { enabled: false })
    expect((await admin.getSearch()).defaultProviderId).toBe('searxng')

    const user = new MockProvidersApi(false)
    await expect(user.setSearchDefault('searxng')).rejects.toThrow(/Admin/)
    const mine = await user.create({ scope: 'personal', presetId: 'tavily', apiKey: 'sk-fake-search-654321' })
    await expect(user.update(mine.id, { options: { max_results: '50' } })).rejects.toThrow(/Results/)
    expect((await user.update(mine.id, { options: { max_results: '7' } })).options).toEqual({ max_results: '7' })
    expect(await user.setMySearch(mine.id)).toMatchObject({ myProviderId: mine.id, effectiveProviderId: mine.id })
    await expect(user.addModel(mine.id, { upstream: 'x' })).rejects.toThrow(/serve no chat models/)
    expect(await user.test('searxng')).toMatchObject({ ok: true })
    await user.remove(mine.id)
    expect((await user.getSearch()).myProviderId).toBe('')
  })
})
