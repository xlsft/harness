// @vitest-environment happy-dom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { VueWrapper } from '@vue/test-utils'
import { MockExtensionsApi, type MockScenario } from '~/lib/extensions/mock'
import { MockMcpApi } from '~/lib/mcp/mock'
import { MockPluginsApi } from '~/lib/plugins/mock'
import { mockSlashCatalog } from '~/lib/transport/mockSlash'
import { loadComponents, mountExtensions, settle, stubNuxtGlobals } from './helpers/mountPage'

let wrapper: VueWrapper
let openSettings: ReturnType<typeof vi.fn>
let plugins: MockPluginsApi

async function open(query: Record<string, string>, scenario: MockScenario = 'default') {
  plugins = new MockPluginsApi()
  const mcp = new MockMcpApi()
  const disabled = new Set<string>()
  const extensions = new MockExtensionsApi(plugins, mcp, () => mockSlashCatalog(disabled), scenario)
  const transport = {
    extensions: () => extensions,
    plugins: () => plugins,
    mcp: () => mcp,
    listRecentFolders: async () => [{ path: '/work/repo' }],
    setSkillEnabled: async (name: string, on: boolean) => {
      if (on) disabled.delete(name)
      else disabled.add(name)
    }
  }
  openSettings = vi.fn()
  stubNuxtGlobals({ transport, query, openSettings })
  wrapper = await mountExtensions()
}

/** Everything on the page and in its sheets: the sheet is teleported in the browser and stubbed in place here. */
const text = () => wrapper.text()

async function click(label: string) {
  const el = wrapper.findAll('button,[role=menuitem],[role=menuitemradio],[role=tab]').find(b => (b.attributes('aria-label') ?? b.text()).trim().startsWith(label))
  if (!el) throw new Error(`no ${label} in: ${text()}`)
  await el.trigger('click')
  await settle()
}

// The first load transforms every component.
beforeAll(() => loadComponents(), 60_000)

beforeEach(() => {
  vi.stubGlobal('confirm', () => true)
  vi.stubGlobal('window', Object.assign(window, { confirm: () => true }))
})

afterEach(() => {
  wrapper?.unmount()
  vi.unstubAllGlobals()
})

describe('the row', () => {
  it('holds tabs, the switch, search, filter, sort and Add, and stays pinned', async () => {
    await open({})
    const row = wrapper.get('[data-pinned-row]')
    expect(row.classes()).toContain('sticky')
    expect(row.findAll('[role=tab]').map(t => t.text())).toEqual(['Skills', 'Connectors', 'Plugins'])
    expect(row.text()).toContain('Yours')
    expect(row.text()).toContain('Discover')
    for (const label of ['Search', 'Filter', 'Sort', 'Add']) expect(row.find(`[aria-label="${label}"]`).exists(), label).toBe(true)
  })

  it('Add offers each way in, and importing goes to Settings > Import', async () => {
    await open({})
    await click('Add')
    expect(text()).toContain('Create a skill')
    expect(text()).toContain('Install from a folder or URL')
    expect(text()).toContain('Add a connector')
    expect(text()).toContain('Add a marketplace')
    await click('Import from Claude Code')
    expect(openSettings).toHaveBeenCalledWith('import')
  })

  it('tabs and the switch change what is shown', async () => {
    await open({})
    await click('Connectors')
    expect(wrapper.find('table[aria-label="Connectors"]').exists()).toBe(true)
    await click('Discover')
    expect(wrapper.find('[data-discover-card]').exists()).toBe(true)
  })
})

describe('Yours: skills', () => {
  it('groups by source with counts, offers a folder, and shows no empty group', async () => {
    await open({ type: 'skills' })
    expect(wrapper.get('[data-project-row]').text()).toContain('Open folder')
    const groups = wrapper.findAll('[data-group]').map(g => g.attributes('data-group'))
    expect(groups).toEqual(['personal', 'external', 'plugin:format-tools@acme', 'organization'])
    expect(wrapper.get('[data-group="personal"]').text()).toContain('tidy-imports')
    expect(text()).not.toContain('None.')
    expect(text()).not.toContain('.claude')
  })

  it('search narrows, and a group left with nothing goes', async () => {
    await open({ type: 'skills' })
    await wrapper.get('input[aria-label="Search"]').setValue('standup')
    await settle()
    expect(wrapper.findAll('[data-group]').map(g => g.attributes('data-group'))).toEqual(['personal'])
    expect(wrapper.findAll('[data-extension-row]')).toHaveLength(1)
  })

  it('creates a skill from the Add menu', async () => {
    await open({ type: 'skills' })
    await click('Add')
    await click('Create a skill')
    const form = wrapper.get('form[aria-label="Create a skill"]')
    const inputs = form.findAll('input')
    await inputs[0]!.setValue('new-one')
    await inputs[1]!.setValue('Does a new thing')
    await form.trigger('submit')
    await settle()
    expect(wrapper.get('[data-group="personal"]').text()).toContain('new-one')
    expect(text()).toContain('.harness/skills')
  })

  it('a skill from search waits for approval, with its audit shown, and the approval takes what was shown', async () => {
    await open({ type: 'skills' })
    expect(wrapper.get('[data-group="external"]').text()).toContain('Needs approval')
    await click('sql-review')
    const approval = wrapper.get('[data-approval]')
    expect(approval.text()).toContain('warn')
    expect(approval.text()).toContain('Runs a shell command')
    await click('Approve on this device')
    expect(wrapper.get('[data-approval]').text()).toContain('Approved as it is now')
  })

  it('says what the administrator set, and offers no switch for it', async () => {
    await open({ type: 'skills' }, 'restricted')
    expect(text()).toContain('Your administrator limits which skills can run')
    const row = wrapper.get('[data-group="organization"]')
    expect(row.text()).toContain('Blocked by your administrator')
    await row.get('[aria-label^="Actions for"]').trigger('click')
    await settle()
    expect(row.text()).not.toContain('Turn on')
  })
})

describe('Yours: connectors', () => {
  it('is one table: type, manager as a badge, status', async () => {
    await open({ type: 'connectors' })
    const rows = wrapper.findAll('[data-connector-row]').map(r => r.text())
    const find = (n: string) => rows.find(r => r.startsWith(n))!
    expect(find('files')).toContain('Local')
    expect(find('files')).toContain('Device')
    expect(find('tracker')).toContain('Account')
    expect(find('tracker')).toContain('Connected')
    expect(find('company')).toContain('Admin')
    expect(find('docs')).toContain('Needs sign-in')
    expect(find('style')).toContain('Needs approval')
  })

  it('folds one server from several plugins into one row', async () => {
    await open({ type: 'connectors' })
    const tasks = wrapper.findAll('[data-connector-row]').filter(r => r.text().startsWith('tasks'))
    expect(tasks).toHaveLength(1)
    expect(tasks[0]!.text()).toContain('Provided by the Docs kit and Plan kit plugins')
    expect(tasks[0]!.text()).toContain('Plugin')
    expect(tasks[0]!.text()).toContain('Connects in sessions')
    await tasks[0]!.trigger('click')
    await settle()
    expect(wrapper.get('[data-providers]').text()).toContain('Open Docs kit')
    expect(wrapper.get('[data-providers]').text()).toContain('Open Plan kit')
  })

  it('filters by who manages it', async () => {
    await open({ type: 'connectors' })
    await click('Filter')
    await click('Admin')
    const rows = wrapper.findAll('[data-connector-row]')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('company')
  })
})

describe('Yours: plugins', () => {
  it('groups by source and lists marketplaces; the built-in one has no Remove', async () => {
    await open({ type: 'plugins' })
    const groups = wrapper.findAll('[data-group]').map(g => g.attributes('data-group'))
    expect(groups).toEqual(['marketplace:acme', 'organization', 'marketplaces'])
    const markets = wrapper.get('[data-group="marketplaces"]')
    expect(markets.text()).toContain('Built in')
    await markets.findAll('[aria-label="Actions for harness-catalog"]')[0]!.trigger('click')
    await settle()
    expect(markets.text()).not.toContain('Remove')
  })

  it('approving shows what it holds and goes through its hash', async () => {
    await open({ type: 'plugins' })
    const spy = vi.spyOn(plugins, 'setApproval')
    await click('Format tools')
    const contents = wrapper.get('[data-contents]')
    expect(contents.text()).toContain('format-tools:format')
    expect(contents.text()).toContain('PostToolUse')
    await click('Approve: let these run with my rights')
    expect(spy).toHaveBeenCalledWith('format-tools@acme', true, 'sha256:mock')
    expect(text()).toContain('Take back approval')
  })

  it('a plugin the organization distributes can\'t be removed', async () => {
    await open({ type: 'plugins' })
    await click('Org standards')
    expect(text()).toContain('Set by your administrator')
    expect(wrapper.findAll('button').map(b => b.text())).not.toContain('Remove')
  })
})

describe('Discover', () => {
  it('features the catalog, with cards and categories', async () => {
    await open({ type: 'skills', view: 'discover' })
    expect(wrapper.get('[data-featured]').text()).toContain('starter:main')
    expect(wrapper.get('[data-section="categories"]').text()).toContain('Getting started')
    expect(wrapper.find('[aria-label="format-tools:main is added"]').exists()).toBe(true)
    expect(wrapper.find('[aria-label="Add release-notes:main"]').exists()).toBe(true)
  })

  it('shows numbers and audits only where the source gives them', async () => {
    await open({ type: 'skills', view: 'discover' })
    const external = wrapper.get('[data-section="external"]').text()
    expect(external).toContain('18.2K installs')
    expect(external).toContain('Audit: pass')
    expect(external).toContain('Audit: warn')
    expect(external).toContain('Audit: fail')
    const catalog = wrapper.get('[data-section="market:harness-catalog"]').text()
    expect(catalog).not.toContain('install')
    expect(catalog).not.toContain('Audit')
    expect(text()).not.toMatch(/verified|for you/i)
  })

  it('no categories for connectors, and search reaches across sections', async () => {
    await open({ type: 'connectors', view: 'discover' })
    expect(wrapper.find('[data-section="categories"]').exists()).toBe(false)
    await wrapper.get('input[aria-label="Search"]').setValue('tickets')
    await new Promise(r => setTimeout(r, 300))
    await settle()
    expect(wrapper.find('[data-featured]').exists()).toBe(false)
    const cards = wrapper.findAll('[data-discover-card]')
    expect(cards).toHaveLength(1)
    expect(cards[0]!.text()).toContain('Example tickets')
  })

  it('a category narrows, and Show all opens a section whole', async () => {
    await open({ type: 'plugins', view: 'discover' })
    await click('Show all')
    expect(wrapper.findAll('[data-section="market:acme"] [data-discover-card]')).toHaveLength(8)
    await click('All')
    await click('Code quality')
    expect(wrapper.findAll('[data-discover-card]')).toHaveLength(1)
  })

  it('installing a plugin goes to its approval: nothing runs yet', async () => {
    await open({ type: 'plugins', view: 'discover' })
    await click('Add release-notes')
    expect(text()).toContain('Nothing of it runs until you approve it')
    expect(text()).toContain('Approve')
    const st = await plugins.list()
    expect(st.plugins.find(p => p.id === 'release-notes@acme')?.approved).toBe(false)
  })

  it('a connector opens what it would run before anything is added', async () => {
    await open({ type: 'connectors', view: 'discover' })
    await click('Add Example tickets')
    const option = wrapper.get('[data-option]')
    expect(option.text()).toContain('npx -y @example/tickets@1.2.0')
    expect(option.text()).toContain('Needs EXAMPLE_TOKEN')
    await click('Set up')
    const form = wrapper.get('form[aria-label="Add an MCP server"]')
    expect((form.find('input').element as HTMLInputElement).value).toBe('tickets')
  })

  it('storing from search pins the hash that was shown, and it then waits for approval', async () => {
    await open({ type: 'skills', view: 'discover' })
    await click('Add pdf-tools')
    expect(wrapper.get('[data-audits]').text()).toContain('pass')
    expect(text()).toContain('Would pin')
    expect(text()).toContain('MIT')
    await click('Store, pinned to this hash')
    expect(wrapper.get('[data-approval]').text()).toContain('doesn\'t load until you approve')
  })

  it('an unaudited skill says that isn\'t a pass', async () => {
    await open({ type: 'skills', view: 'discover' })
    await click('Add unaudited')
    expect(wrapper.get('[data-audits]').text()).toContain('That isn\'t a pass')
  })

  it('works offline on what it has, and says so', async () => {
    await open({ type: 'connectors', view: 'discover' }, 'offline')
    expect(text()).toContain('can\'t be reached')
    expect(wrapper.find('[data-section="catalog"]').exists()).toBe(true)
    expect(wrapper.find('[data-section="registry"]').exists()).toBe(false)
  })

  it('what policy refuses can\'t be added, and external search hides', async () => {
    await open({ type: 'connectors', view: 'discover' }, 'restricted')
    expect((wrapper.get('[aria-label="Add Linear"]').element as HTMLButtonElement).disabled).toBe(true)
    wrapper.unmount()
    await open({ type: 'skills', view: 'discover' }, 'restricted')
    expect(text()).toContain('Your administrator turned external skill search off')
    expect(wrapper.find('[data-section="external"]').exists()).toBe(false)
  })

  it('a first run shows the built-in catalog alone', async () => {
    await open({ type: 'plugins', view: 'discover' }, 'first-run')
    expect(wrapper.get('[data-featured]').text()).toContain('starter')
    expect(wrapper.findAll('[data-section]').map(s => s.attributes('data-section'))).toEqual(['market:harness-catalog', 'categories'])
  })
})
