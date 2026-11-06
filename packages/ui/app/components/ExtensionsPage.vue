<script setup lang="ts">
// Extensions: type tabs, Yours or Discover, and a row of search, filter, sort and Add that stays pinned while the page scrolls. The
// client joins and decides (ExtensionsService); this page keeps what is being looked at, in the URL so Back and links work, and sends
// intent. Plugins and connectors change through PluginsApi and McpApi, with their approvals.
import { isDesktop, pickFolder } from '~/lib/desktop'
import { ExtensionKind, type DiscoverExtensionsResponse, type DiscoverItem, type ExtensionConnector, type ExtensionMarketplace, type ExtensionPluginsVM, type ExtensionPluginVM, type ExtensionSkill, type ExtensionType, type ExtensionView, type ListExtensionConnectorsResponse, type ListExtensionSkillsResponse } from '~/lib/extensions/types'
import { narrowConnectors, narrowPlugins, narrowSkills, parseType, parseView } from '~/lib/extensions/view'
import type { FilterMenu } from '~/components/extensions/ExtensionsToolbar.vue'
import type { AddMode } from '~/components/extensions/ExtensionsAdd.vue'
import type { DetailTarget } from '~/components/extensions/ExtensionsDetail.vue'
import type { ConnectorOption } from '~/lib/extensions/types'

const route = useRoute()
const router = useRouter()
const store = useSessionStore()
const settingsModal = useSettingsModal()

const type = computed<ExtensionType>(() => parseType(route.query.type))
const view = computed<ExtensionView>(() => parseView(route.query.view))
function go(next: { type?: ExtensionType, view?: ExtensionView }) {
  const { settings } = route.query
  void router.replace({ query: { type: next.type ?? type.value, view: next.view ?? view.value, ...(settings ? { settings } : {}) } })
}

const cwd = ref(store.current?.meta.noFolder ? '' : store.current?.meta.cwd ?? '')
const query = ref('')
const sort = ref('')
const error = ref('')
const note = ref('')
const loading = ref(false)
const detail = ref<DetailTarget | null>(null)
const addMode = ref<AddMode | null>(null)
const addOption = ref<ConnectorOption | null>(null)
const folders = ref<string[]>([])

const skills = ref<ListExtensionSkillsResponse | null>(null)
const plugins = ref<ExtensionPluginsVM | null>(null)
const connectors = ref<ListExtensionConnectorsResponse | null>(null)
const discover = ref<DiscoverExtensionsResponse | null>(null)

// The filters of each view, cleared when it changes.
const filter = reactive({ source: '', state: '', manager: '', connectorType: '', status: '', category: '', hideInstalled: '', section: '' })
function clearFilters() {
  Object.assign(filter, { source: '', state: '', manager: '', connectorType: '', status: '', category: '', hideInstalled: '', section: '' })
  sort.value = ''
  query.value = ''
}

const KINDS: Record<ExtensionType, ExtensionKind> = { skills: ExtensionKind.SKILL, connectors: ExtensionKind.CONNECTOR, plugins: ExtensionKind.PLUGIN }

function api() {
  return store.transport!.extensions()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

let asked = 0
async function load(opts: { refresh?: boolean, quiet?: boolean } = {}) {
  if (!store.transport) return
  const n = ++asked
  if (!opts.quiet) loading.value = true
  try {
    if (view.value === 'discover') {
      const res = await api().discover({ kind: KINDS[type.value], query: query.value, category: filter.category, section: filter.section, source: filter.source, connectorType: filter.connectorType, hideInstalled: filter.hideInstalled === 'yes', sort: sort.value, refresh: opts.refresh, cwd: cwd.value })
      if (n === asked) discover.value = res
    } else if (type.value === 'skills') {
      const res = await api().skills(cwd.value)
      if (n === asked) skills.value = res
    } else if (type.value === 'plugins') {
      const res = await api().plugins(cwd.value)
      if (n === asked) plugins.value = res
    } else {
      const res = await api().connectors(cwd.value)
      if (n === asked) connectors.value = res
    }
    if (n === asked) error.value = ''
  } catch (e) {
    if (n === asked) error.value = message(e)
  } finally {
    if (n === asked) loading.value = false
  }
}

watch([type, view], () => {
  clearFilters()
  void load()
})
watch(cwd, () => load())
watch([sort, () => filter.source, () => filter.connectorType, () => filter.hideInstalled, () => filter.category, () => filter.section], () => {
  if (view.value === 'discover') void load({ quiet: true })
})
// Search in Discover goes to the client, which asks its sources: not on every key.
let typing: ReturnType<typeof setTimeout> | undefined
watch(query, () => {
  if (view.value !== 'discover') return
  clearTimeout(typing)
  typing = setTimeout(() => load({ quiet: true }), 250)
})
onBeforeUnmount(() => clearTimeout(typing))

onMounted(async () => {
  await load()
  try {
    folders.value = ((await store.transport?.listRecentFolders()) ?? []).map(f => f.path)
  } catch { /* the folder row still takes a path */ }
})

// Runs a change, says what came of it and reads the list again.
async function act(f: () => Promise<unknown>, done = '') {
  note.value = ''
  try {
    const said = await f()
    note.value = done || (typeof said === 'string' ? said : '')
    error.value = ''
  } catch (e) {
    error.value = message(e)
    await load({ quiet: true })
    throw e
  }
  await load({ quiet: true })
}
// For menus, where the failure has already been said.
const quietly = (f: () => Promise<unknown>, done = '') => act(f, done).catch(() => {})

async function openFolder() {
  if (isDesktop()) {
    const picked = await pickFolder(cwd.value)
    if (picked) cwd.value = picked
    return
  }
  addMode.value = 'folder'
}
function trustFolder() {
  return quietly(() => store.transport!.mcp().setTrust(cwd.value, true), 'This folder is trusted now.')
}

// ---- what the page shows ----
const projectRow = computed(() => view.value === 'yours')
const trusted = computed(() => {
  if (type.value === 'skills') return skills.value?.folderTrusted ?? false
  if (type.value === 'plugins') return plugins.value?.folderTrusted ?? false
  return connectors.value?.projectTrusted ?? false
})

const allSkillGroups = computed(() => {
  const res = skills.value
  if (!res) return []
  const groups = [...res.groups]
  // Stored from search and waiting for this device: they belong with the others from there.
  if (res.waiting.length) {
    const i = groups.findIndex(g => g.key === 'external')
    if (i >= 0) groups[i] = { ...groups[i]!, skills: [...res.waiting, ...groups[i]!.skills] }
    else groups.splice(groups.findIndex(g => g.key !== 'project' && g.key !== 'personal') < 0 ? groups.length : groups.findIndex(g => g.key !== 'project' && g.key !== 'personal'), 0, { $typeName: 'harness.agent.v1.ExtensionSkillGroup', key: 'external', title: 'skills.sh', skills: res.waiting })
  }
  return groups
})
const skillGroups = computed(() => narrowSkills(allSkillGroups.value, { query: query.value, source: filter.source, state: filter.state, sort: sort.value }))
const pluginGroups = computed(() => narrowPlugins(plugins.value?.groups ?? [], { query: query.value, source: filter.source, state: filter.state, sort: sort.value }))
const connectorRows = computed(() => narrowConnectors(connectors.value?.connectors ?? [], { query: query.value, manager: filter.manager, type: filter.connectorType, status: filter.status }))
const shownMarketplaces = computed(() => {
  const q = query.value.toLowerCase()
  return (filter.source || filter.state) ? [] : (plugins.value?.marketplaces ?? []).filter(m => !q || `${m.name} ${m.source} ${m.description}`.toLowerCase().includes(q))
})

const stateOptions = [{ value: '', label: 'Any state' }, { value: 'on', label: 'On' }, { value: 'off', label: 'Off' }, { value: 'attention', label: 'Needs attention' }]
const filters = computed<FilterMenu[]>(() => {
  if (view.value === 'discover') {
    const out: FilterMenu[] = []
    if (discover.value?.sources.length) out.push({ key: 'source', title: 'Source', options: [{ value: '', label: 'Any source' }, ...discover.value.sources.map(s => ({ value: s, label: s }))] })
    if (type.value === 'connectors') out.push({ key: 'connectorType', title: 'Type', options: [{ value: '', label: 'Any type' }, { value: 'local', label: 'Local' }, { value: 'remote', label: 'Remote' }] })
    out.push({ key: 'hideInstalled', title: 'Added', options: [{ value: '', label: 'Show all' }, { value: 'yes', label: 'Hide what I have' }] })
    return out
  }
  if (type.value === 'connectors') {
    return [
      { key: 'manager', title: 'Managed by', options: [{ value: '', label: 'Anyone' }, { value: 'Device', label: 'This device' }, { value: 'Account', label: 'My account' }, { value: 'Admin', label: 'Admin' }, { value: 'Repository', label: 'Repository' }, { value: 'Plugin', label: 'A plugin' }] },
      { key: 'connectorType', title: 'Type', options: [{ value: '', label: 'Any type' }, { value: 'local', label: 'Local' }, { value: 'remote', label: 'Remote' }] },
      { key: 'status', title: 'Status', options: [{ value: '', label: 'Any status' }, { value: 'connected', label: 'Connected' }, { value: 'attention', label: 'Needs attention' }] }
    ]
  }
  const groups = type.value === 'skills' ? allSkillGroups.value.map(g => ({ key: g.key, title: g.title })) : (plugins.value?.groups ?? []).map(g => ({ key: g.key, title: g.title }))
  return [
    { key: 'source', title: 'Source', options: [{ value: '', label: 'Any source' }, ...groups.map(g => ({ value: g.key, label: g.title }))] },
    { key: 'state', title: 'State', options: stateOptions }
  ]
})
const sortOptions = computed(() => {
  if (view.value === 'discover') return type.value === 'connectors' ? [{ value: '', label: 'As listed' }, { value: 'name', label: 'Name' }, { value: 'updated', label: 'Recently updated' }] : [{ value: '', label: 'As listed' }, { value: 'name', label: 'Name' }]
  return type.value === 'connectors' ? [] : [{ value: '', label: 'As listed' }, { value: 'name', label: 'Name' }, { value: 'recent', label: 'Recently added' }]
})
const filterValues = computed<Record<string, string>>(() => ({ ...filter }))
function setFilter(key: string, value: string) {
  (filter as Record<string, string>)[key] = value
}

const addItems = computed(() => [
  { label: 'Create a skill…', run: () => { addMode.value = 'skill' } },
  { label: 'Install from a folder or URL…', run: () => { addMode.value = 'install' } },
  { label: 'Add a connector…', run: () => addConnector(null) },
  { label: 'Add a marketplace…', run: () => { addMode.value = 'marketplace' } },
  null,
  { label: 'Import from Claude Code…', run: () => settingsModal.open('import') }
])
function addConnector(option: ConnectorOption | null) {
  addOption.value = option
  addMode.value = 'connector'
}

// ---- Discover ----
async function addFromDiscover(it: DiscoverItem) {
  if (it.options.length || it.externalId || it.problem) {
    detail.value = { kind: 'discover', item: it }
    return
  }
  await quietly(async () => {
    const p = await store.transport!.plugins().install(it.pluginId, cwd.value)
    detail.value = { kind: 'plugin', row: { plugin: p, category: it.category, tags: it.tags, publisher: it.publisher, homepage: it.homepage, license: it.license, folder: '' } }
    return `Installed ${p.id}. Nothing of it runs until you approve it.`
  })
}

function openPlugin(id: string) {
  const row = plugins.value?.groups.flatMap(g => g.plugins).find(p => p.plugin.id === id)
  if (row) {
    detail.value = { kind: 'plugin', row }
    return
  }
  void (async () => {
    const res = await api().plugins(cwd.value)
    const found = res.groups.flatMap(g => g.plugins).find(p => p.plugin.id === id)
    if (found) detail.value = { kind: 'plugin', row: found }
  })()
}

function openSkill(row: ExtensionSkill) {
  detail.value = { kind: 'skill', row }
}
function openConnector(row: ExtensionConnector) {
  detail.value = { kind: 'connector', row }
}
function openPluginRow(row: ExtensionPluginVM) {
  detail.value = { kind: 'plugin', row }
}
function openMarketplace(m: ExtensionMarketplace) {
  detail.value = { kind: 'marketplace', market: m }
}

async function moreFromSection(cursor: string, key: string) {
  const res = await api().discover({ kind: KINDS[type.value], query: query.value, category: filter.category, section: key, source: filter.source, connectorType: filter.connectorType, hideInstalled: filter.hideInstalled === 'yes', sort: sort.value, cursor, cwd: cwd.value })
  if (!discover.value) return
  const add = res.sections.find(s => s.key === key)
  for (const s of discover.value.sections) {
    if (s.key === key) {
      s.items = [...s.items, ...(add?.items ?? [])]
      s.nextCursor = add?.nextCursor ?? ''
      s.total = s.items.length
    }
  }
}

function afterChange() {
  void load({ quiet: true })
}
function added(said: string) {
  addMode.value = null
  addOption.value = null
  note.value = said
  void load({ quiet: true })
}
</script>

<template>
  <section
    aria-label="Extensions"
    class="overflow-y-auto bg-default"
  >
    <div class="mx-auto w-full max-w-[1040px] px-10 max-sm:px-4">
      <h1 class="m-0 pt-3 pb-4 font-display text-[26px] font-bold text-strong">
        Extensions
      </h1>
    </div>

    <ExtensionsToolbar
      v-model:query="query"
      v-model:sort="sort"
      :type="type"
      :view="view"
      :filters="filters"
      :filter-values="filterValues"
      :sort-options="sortOptions"
      :add-items="addItems"
      @update:type="go({ type: $event })"
      @update:view="go({ view: $event })"
      @filter="setFilter"
    />

    <div class="mx-auto flex w-full max-w-[1040px] flex-col gap-6 px-10 pt-5 pb-12 max-sm:px-4">
      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>
      <p
        v-if="note"
        role="status"
        class="m-0 border border-l-2 border-default border-l-accent px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ note }}
      </p>

      <template v-if="view === 'yours'">
        <p
          v-if="type === 'skills' && skills?.policyNote"
          class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12.5px] text-toned"
        >
          {{ skills.policyNote }}
        </p>
        <p
          v-if="type === 'plugins' && plugins?.sourcesRestricted"
          class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12.5px] text-toned"
        >
          Your administrator allows plugins only from the sources they list.
        </p>
        <p
          v-if="type === 'connectors' && connectors?.registryError"
          class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12.5px] text-toned"
        >
          Connectors from your account and your admin can't be listed: {{ connectors.registryError }}
        </p>
        <p
          v-if="type === 'connectors' && connectors?.projectError"
          class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
        >
          {{ connectors.projectError }}
        </p>

        <ExtensionsProjectRow
          v-if="projectRow"
          :cwd="cwd"
          :trusted="trusted"
          :what="type"
          :waiting="type === 'skills' && !!skills?.projectSkippedUntrusted"
          @open="openFolder"
          @trust="trustFolder"
          @close="cwd = ''"
        />

        <ExtensionsYoursSkills
          v-if="type === 'skills'"
          :groups="skillGroups"
          :cwd="cwd"
          :act="quietly"
          @open="openSkill"
          @open-plugin="openPlugin"
        />
        <ExtensionsYoursPlugins
          v-else-if="type === 'plugins'"
          :groups="pluginGroups"
          :marketplaces="shownMarketplaces"
          :suggested="plugins?.suggestedMarketplaces ?? {}"
          :cwd="cwd"
          :act="quietly"
          @open="openPluginRow"
          @open-marketplace="openMarketplace"
        />
        <ExtensionsConnectorTable
          v-else
          :rows="connectorRows"
          :cwd="cwd"
          :act="quietly"
          @open="openConnector"
          @add="addConnector(null)"
        />
        <HLoader
          v-if="loading"
          class="text-muted"
        />
      </template>

      <ExtensionsDiscover
        v-else
        :data="discover"
        :type="type"
        :category="filter.category"
        :section="filter.section"
        :loading="loading"
        :query="query"
        @add="addFromDiscover"
        @open="detail = { kind: 'discover', item: $event }"
        @category="filter.category = $event"
        @section="filter.section = $event"
        @more="moreFromSection"
        @refresh="load({ refresh: true })"
      />
    </div>

    <ExtensionsDetail
      v-if="detail"
      :target="detail"
      :cwd="cwd"
      :can-manage-org="connectors?.canManageOrg ?? false"
      @close="detail = null"
      @changed="afterChange"
      @open-plugin="openPlugin"
      @add-connector="(o: ConnectorOption) => { detail = null; addConnector(o) }"
      @target="detail = $event"
    />
    <ExtensionsAdd
      v-if="addMode"
      :mode="addMode"
      :cwd="cwd"
      :trusted="trusted"
      :folders="folders"
      :option="addOption"
      :can-manage-org="connectors?.canManageOrg ?? false"
      @close="addMode = null; addOption = null"
      @done="added"
      @folder="cwd = $event; addMode = null"
    />
  </section>
</template>
