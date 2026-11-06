<script setup lang="ts">
// Yours > Skills: rows under their sources. A skill's switch follows the user across machines; one stored from search also waits for this
// device's approval, which is in its detail.
import type { RowMenuItem } from '~/components/RowMenu.vue'
import { isDesktop, revealInFolder } from '~/lib/desktop'
import type { ExtensionSkill, ExtensionSkillGroup } from '~/lib/extensions/types'
import { auditSummary, shortDate, skillTags, type StatusView } from '~/lib/extensions/view'

const props = defineProps<{ groups: ExtensionSkillGroup[], cwd: string, act: (f: () => Promise<unknown>, done?: string) => Promise<unknown> }>()
const emit = defineEmits<{ 'open': [ExtensionSkill], 'open-plugin': [string] }>()
const store = useSessionStore()

function rowName(s: ExtensionSkill) {
  return s.command?.name ?? s.external?.name ?? ''
}

function statusOf(s: ExtensionSkill): StatusView | null {
  if (s.external && !s.external.approved) return s.external.changedOnDisk ? { label: 'Changed on disk', tone: 'bad' } : { label: 'Needs approval', tone: 'warn' }
  if (s.blockedByPolicy) return { label: 'Blocked by your administrator', tone: 'muted' }
  if (s.command && !s.command.enabled) return { label: 'Off', tone: 'muted' }
  if (s.command?.shadowedBy) return { label: 'Replaced', tone: 'muted' }
  return s.external ? auditSummary(s.external.audits) : null
}

function menu(s: ExtensionSkill): (RowMenuItem | null)[] {
  const items: (RowMenuItem | null)[] = [{ label: 'Open', run: () => emit('open', s) }]
  const c = s.command
  if (c?.switchable && !s.blockedByPolicy) {
    items.push({ label: c.enabled ? 'Turn off' : 'Turn on', disabled: !!c.shadowedBy, run: () => props.act(() => store.transport!.setSkillEnabled(c.name, !c.enabled)) })
  }
  if (s.pluginId) items.push({ label: 'Open its plugin', run: () => emit('open-plugin', s.pluginId) })
  if (s.folder && isDesktop()) items.push({ label: 'Show in folder', run: () => revealInFolder(s.folder) })
  if (s.external) {
    items.push(null, { label: 'Remove', run: () => confirmRemove(s.external!.name, () => store.transport!.extensions().removeExternal(s.external!.name)) })
  } else if (s.canRemove && c) {
    items.push(null, { label: 'Remove', run: () => confirmRemove(c.name, () => store.transport!.extensions().removeSkill(c.name, c.path, props.cwd)) })
  }
  return items
}

function confirmRemove(name: string, f: () => Promise<unknown>) {
  if (!window.confirm(`Delete ${name} from this device? Its files go too.`)) return
  return props.act(f)
}
</script>

<template>
  <ExtensionsGroup
    v-for="g in groups"
    :key="g.key"
    :title="g.title"
    :count="g.skills.length"
    :data-group="g.key"
  >
    <ExtensionsRow
      v-for="s in g.skills"
      :key="`${rowName(s)}:${s.command?.path}`"
      icon="page"
      :name="rowName(s)"
      :tags="skillTags(s)"
      :source="`from ${g.title}`"
      :description="s.command?.description ?? s.external?.description"
      :date="shortDate(s.updatedAt || s.external?.installedAt || '')"
      :status="statusOf(s)"
      :dim="!!s.command?.shadowedBy || (s.command && !s.command.enabled) || s.blockedByPolicy"
      :items="menu(s)"
      @open="emit('open', s)"
    />
  </ExtensionsGroup>
</template>
