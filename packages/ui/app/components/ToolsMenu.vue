<script setup lang="ts">
// Tools menu (HeaderMenu.dc.html): opens or closes a right pane; the check marks the panes open now. Keep
// computer awake holds off sleep while this session works: the client does it, the switch shows what it reports, and
// keepAwakeAvailable says who gets the switch.
import type { IconName } from '~/utils/icons'
import type { PaneName } from '~/composables/usePanes'
import { isDesktop } from '~/lib/desktop'

const emit = defineEmits<{ select: [] }>()
const { isOpen, togglePane } = usePanes()
const store = useSessionStore()
const current = computed(() => store.sessions[store.currentId])
const showAwake = computed(() => !!current.value && keepAwakeAvailable(current.value.meta, { desktop: isDesktop(), controlling: !!store.remoteControl[store.currentId] }))
const awake = computed({
  get: () => !!current.value?.meta.keepAwake,
  set: (on: boolean) => {
    if (store.currentId) store.quietly(store.setKeepAwake(store.currentId, on))
  }
})

// Panes of features the session's engine has not got are not offered.
const FEATURE_OF: Partial<Record<PaneName, string>> = { sidechat: 'side_chat', tasks: 'background_tasks', agents: 'sub_sessions' }
const allItems: { pane: PaneName, label: string, icon: IconName, kbd?: string }[] = [
  { pane: 'terminal', label: 'Terminal', icon: 'term', kbd: 'meta+j' },
  { pane: 'browser', label: 'Browser', icon: 'globe-stand', kbd: 'meta+shift+b' },
  { pane: 'files', label: 'Files', icon: 'folder', kbd: 'meta+shift+e' },
  { pane: 'diff', label: 'Changes', icon: 'diff' },
  { pane: 'sidechat', label: 'Side chat', icon: 'chat', kbd: 'meta+;' },
  { pane: 'tasks', label: 'Background tasks', icon: 'diamond' },
  { pane: 'agents', label: 'Agents', icon: 'ai' }
]
const items = computed(() => allItems.filter(i => !current.value?.meta.unsupported.includes(FEATURE_OF[i.pane] ?? '')))

function choose(p: PaneName) {
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
      :aria-checked="isOpen(m.pane)"
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
        v-if="isOpen(m.pane)"
        name="check"
        :size="14"
        class="text-accent"
      />
    </div>
    <template v-if="showAwake">
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
