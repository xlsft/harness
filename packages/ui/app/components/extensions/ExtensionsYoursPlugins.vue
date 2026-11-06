<script setup lang="ts">
// Yours > Plugins: installed plugins under their sources, then the marketplaces they come from. Plugins are this device's, and the client
// says what may run; a plugin waiting for approval says so on its row and is approved in its detail, where what it holds is shown.
import type { RowMenuItem } from '~/components/RowMenu.vue'
import { isDesktop, revealInFolder } from '~/lib/desktop'
import type { ExtensionMarketplace, ExtensionPluginGroupVM, ExtensionPluginVM } from '~/lib/extensions/types'
import { shortDate, type StatusView } from '~/lib/extensions/view'
import { statusLabel, type PluginVM } from '~/lib/plugins/types'

const props = defineProps<{
  groups: ExtensionPluginGroupVM[]
  marketplaces: ExtensionMarketplace[]
  suggested: Record<string, string>
  cwd: string
  act: (f: () => Promise<unknown>, done?: string) => Promise<unknown>
}>()
const emit = defineEmits<{ 'open': [ExtensionPluginVM], 'open-marketplace': [ExtensionMarketplace] }>()
const store = useSessionStore()
const api = () => store.transport!.plugins()

function statusOf(p: PluginVM): StatusView | null {
  switch (p.status) {
    case 'active': return null
    case 'disabled': return { label: statusLabel(p.status), tone: 'muted' }
    case 'blocked_by_policy': return { label: 'Blocked by your administrator', tone: 'muted' }
    case 'broken':
    case 'changed_on_disk': return { label: statusLabel(p.status), tone: 'bad' }
    default: return { label: statusLabel(p.status), tone: 'warn' }
  }
}

function menu(row: ExtensionPluginVM): (RowMenuItem | null)[] {
  const p = row.plugin
  if (p.status === 'not_installed') {
    return [{ label: 'Open', run: () => emit('open', row) }, { label: 'Install', run: () => props.act(() => api().install(p.id, props.cwd), `Installed ${p.id}. Nothing of it runs until you approve it.`) }]
  }
  const items: (RowMenuItem | null)[] = [
    { label: p.approved ? 'Open' : 'Open and approve', run: () => emit('open', row) },
    { label: p.enabled ? 'Turn off' : 'Turn on', disabled: p.managed, run: () => props.act(() => api().setEnabled(p.id, !p.enabled)) },
    { label: 'Update', run: () => props.act(async () => {
      const res = await api().update(p.id, props.cwd)
      return res.changed ? `${p.id} is now ${res.plugin?.version}. Read what it holds and approve it again.` : res.note
    }) },
    { label: p.pinned ? 'Unpin' : 'Pin this version', run: () => props.act(() => api().setPinned(p.id, !p.pinned)) }
  ]
  if (row.folder && isDesktop()) items.push({ label: 'Show in folder', run: () => revealInFolder(row.folder) })
  if (!p.managed) {
    items.push(null, { label: 'Remove', run: () => {
      if (window.confirm(`Remove ${p.id} and its data folder?`)) return props.act(() => api().remove(p.id, false))
    } })
  }
  return items
}

function marketMenu(m: ExtensionMarketplace): (RowMenuItem | null)[] {
  const items: (RowMenuItem | null)[] = [
    { label: 'Open', run: () => emit('open-marketplace', m) },
    { label: 'Update', disabled: m.builtin, run: () => props.act(() => api().updateMarketplace(m.name), `${m.name} is up to date.`) }
  ]
  if (!m.managed && !m.builtin) {
    items.push(null, { label: 'Remove', run: () => {
      if (window.confirm(`Remove the marketplace ${m.name} and the plugins installed from it?`)) return props.act(() => api().removeMarketplace(m.name))
    } })
  }
  return items
}

const suggestedRows = computed(() => Object.entries(props.suggested))
const hasMarkets = computed(() => props.marketplaces.length + suggestedRows.value.length)
</script>

<template>
  <ExtensionsGroup
    v-for="g in groups"
    :key="g.key"
    :title="g.title"
    :count="g.plugins.length"
    :data-group="g.key"
  >
    <ExtensionsRow
      v-for="row in g.plugins"
      :key="row.plugin.id"
      icon="box"
      :name="row.plugin.displayName || row.plugin.name"
      :tags="[...(row.category ? [row.category] : []), ...row.tags]"
      :source="row.publisher ? `from ${row.publisher}` : `from ${g.title}`"
      :description="row.plugin.description"
      :date="shortDate(row.plugin.installedAt ?? '')"
      :status="statusOf(row.plugin)"
      :dim="row.plugin.status === 'disabled' || row.plugin.status === 'blocked_by_policy'"
      :items="menu(row)"
      @open="emit('open', row)"
    />
  </ExtensionsGroup>

  <ExtensionsGroup
    v-if="hasMarkets"
    title="Marketplaces"
    :count="hasMarkets"
    data-group="marketplaces"
  >
    <ExtensionsRow
      v-for="([name, source]) in suggestedRows"
      :key="`suggested:${name}`"
      icon="download"
      :name="name"
      :tags="['This project asks for it']"
      :source="source"
      :items="[{ label: 'Add', run: () => act(() => api().addMarketplace(source), `Added ${name}. Nothing was installed.`) }]"
      @open="act(() => api().addMarketplace(source), `Added ${name}. Nothing was installed.`)"
    />
    <ExtensionsRow
      v-for="m in marketplaces"
      :key="m.name"
      icon="download"
      :name="m.name"
      :tags="[...(m.builtin ? ['Built in'] : []), ...(m.managed ? ['Admin'] : [])]"
      :source="m.builtin ? 'ships with this client' : m.source"
      :description="m.refusal || m.problem || `${m.pluginCount} plugin${m.pluginCount === 1 ? '' : 's'}, ${m.installedCount} installed`"
      :date="shortDate(m.updatedAt)"
      :items="marketMenu(m)"
      @open="emit('open-marketplace', m)"
    />
  </ExtensionsGroup>
</template>
