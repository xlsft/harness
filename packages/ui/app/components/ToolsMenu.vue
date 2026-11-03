<script setup lang="ts">
// Tools menu (HeaderMenu.dc.html): opens or closes a right pane; the check marks the panes open now. Keep
// computer awake (desktop) holds off sleep while this session works.
import type { IconName } from '~/utils/icons'
import type { PaneName } from '~/composables/usePanes'
import { isDesktop } from '~/lib/desktop'

const emit = defineEmits<{ select: [] }>()
const { isOpen, togglePane } = usePanes()
const store = useSessionStore()
const awakeSessions = useState<string[]>('keep-awake', () => [])
const desktop = isDesktop()
const awake = computed({
  get: () => !!store.currentId && awakeSessions.value.includes(store.currentId),
  set: (on: boolean) => {
    const id = store.currentId
    if (!id) return
    awakeSessions.value = on ? [...new Set([...awakeSessions.value, id])] : awakeSessions.value.filter(x => x !== id)
  }
})

const items: { pane: PaneName | 'ios', label: string, icon: IconName, kbd?: string }[] = [
  { pane: 'terminal', label: 'Terminal', icon: 'term', kbd: 'meta+j' },
  { pane: 'browser', label: 'Browser', icon: 'globe-stand', kbd: 'meta+shift+b' },
  { pane: 'files', label: 'Files', icon: 'folder', kbd: 'meta+shift+e' },
  { pane: 'sidechat', label: 'Side chat', icon: 'chat', kbd: 'meta+;' },
  { pane: 'tasks', label: 'Background tasks', icon: 'diamond' },
  { pane: 'agents', label: 'Agents', icon: 'ai' },
  { pane: 'ios', label: 'iOS Simulator', icon: 'phone' }
]

function choose(p: PaneName | 'ios') {
  if (p === 'ios') return
  togglePane(p)
  emit('select')
}
</script>

<template>
  <div
    role="menu"
    aria-label="Tools"
    class="menu-panel w-[290px] overflow-hidden p-0"
  >
    <div
      v-for="m in items"
      :key="m.pane"
      role="menuitemcheckbox"
      :aria-checked="m.pane !== 'ios' && isOpen(m.pane)"
      class="flex cursor-pointer items-center gap-2.5 rounded-none px-3 py-[7px] hover:bg-raised"
      @click="choose(m.pane)"
    >
      <HIcon :name="m.icon" />
      <span class="flex-1">{{ m.label }}</span>
      <HKeys
        v-if="m.kbd"
        :keys="m.kbd"
      />
      <HIcon
        v-if="m.pane !== 'ios' && isOpen(m.pane)"
        name="check"
        :size="14"
        class="text-accent"
      />
    </div>
    <template v-if="desktop">
      <div class="h-px bg-line" />
      <div
        role="menuitemcheckbox"
        :aria-checked="awake"
        class="flex cursor-pointer items-center gap-2.5 rounded-none px-3 py-2 hover:bg-raised"
        @click="awake = !awake"
      >
        <HIcon
          name="sun"
          class="text-muted"
        />
        <span class="flex flex-1 flex-col gap-px"><span class="text-strong">Keep computer awake</span><span class="text-[11px] text-muted">While this session works</span></span>
        <HToggle
          v-model="awake"
          label="Keep computer awake"
          @click.stop
        />
      </div>
    </template>
  </div>
</template>
