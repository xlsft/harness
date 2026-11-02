<script setup lang="ts">
// Tools menu (HeaderMenu.dc.html): opens a right pane; the check marks tools pinned for this session,
// the highlighted row is the pane that is open now.
import type { IconName } from '~/utils/icons'
import type { PaneName } from '~/composables/usePanes'

const emit = defineEmits<{ select: [] }>()
const { pane, togglePane } = usePanes()
const pinned = useState<string[]>('tools-pinned', () => ['artifact', 'tasks', 'agents'])
const awake = useState('keep-awake', () => false)

const items: { pane: PaneName | 'ios', label: string, icon: IconName, kbd?: string }[] = [
  { pane: 'terminal', label: 'Terminal', icon: 'term', kbd: '⌘J' },
  { pane: 'browser', label: 'Browser', icon: 'globe-stand', kbd: '⌘⇧B' },
  { pane: 'files', label: 'Files', icon: 'folder', kbd: '⌘⇧E' },
  { pane: 'artifact', label: 'Artifacts', icon: 'copy' },
  { pane: 'sidechat', label: 'Side chat', icon: 'chat', kbd: '⌘;' },
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
    class="menu-panel w-[290px]"
  >
    <div
      v-for="m in items"
      :key="m.pane"
      role="menuitemcheckbox"
      :aria-checked="pinned.includes(m.pane)"
      class="flex cursor-default items-center gap-2.5 border-l-2 px-[9px] py-[7px] hover:bg-raised"
      :class="pane === m.pane ? 'border-accent bg-raised text-white' : 'border-transparent'"
      @click="choose(m.pane)"
    >
      <HIcon :name="m.icon" />
      <span class="flex-1">{{ m.label }}</span>
      <span class="text-[11.5px] text-muted">{{ m.kbd }}</span>
      <HIcon
        v-if="pinned.includes(m.pane)"
        name="check"
        :size="14"
        class="text-accent"
      />
      <span
        v-else
        class="size-3.5"
      />
    </div>
    <div class="my-1 h-px bg-line" />
    <div
      role="menuitemcheckbox"
      :aria-checked="awake"
      class="mt-0.5 flex cursor-default items-center gap-2.5 bg-raised px-[9px] py-2"
      @click="awake = !awake"
    >
      <HIcon
        name="sun"
        class="text-muted"
      />
      <span class="flex flex-1 flex-col gap-px"><span class="text-white">Keep computer awake</span><span class="text-[11px] text-muted">Only for this session</span></span>
      <HToggle
        v-model="awake"
        label="Keep computer awake"
      />
    </div>
  </div>
</template>
