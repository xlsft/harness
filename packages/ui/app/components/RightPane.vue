<script setup lang="ts">
// One right pane inside the pane layout. Its 44px header is a drag handle (pointer events): drop it on another pane's edge
// to place it beside (left / right) or under / above (top / bottom) that pane, or on its middle to swap.
import type { SessionState } from '~/lib/session/types'
import type { PaneName } from '~/composables/usePanes'
import { isDesktop } from '~/lib/desktop'

const props = defineProps<{ pane: PaneName, session: SessionState }>()
const { menu } = usePanes()
const { drag, startDrag } = usePaneLayout()

const component = computed(() => ({
  details: resolveComponent('DetailsPane'),
  terminal: resolveComponent('TerminalPane'),
  diff: resolveComponent('DiffPane'),
  browser: resolveComponent('BrowserPane'),
  files: resolveComponent('FilesPane'),
  artifact: resolveComponent('ArtifactPane'),
  sidechat: resolveComponent('SideChatPane'),
  tasks: resolveComponent('TasksPane'),
  agents: resolveComponent('AgentsPane'),
  sentfile: resolveComponent('SentFilePane'),
  visual: resolveComponent('VisualPane')
}[props.pane]))

const { isFull, leave } = usePaneFullscreen()
const full = computed(() => props.pane !== 'details' && isFull(props.session.meta.id, props.pane))
provide('pane-full-target', computed(() => props.pane === 'details' ? null : { pane: props.pane, sessionId: props.session.meta.id }))
const desktop = isDesktop()
const TITLES: Record<PaneName, string> = {
  details: tt('Overview'), terminal: tt('Terminal'), diff: tt('Changes'), browser: tt('Browser'), files: tt('Files'),
  artifact: tt('Artifacts'), sidechat: tt('Side chat'), tasks: tt('Tasks'), agents: tt('Agents'), sentfile: tt('File'), visual: tt('Visual')
}
function onFullKey(e: KeyboardEvent) {
  if (e.key === 'Escape') leave(props.session.meta.id)
}
watch(full, (v) => {
  if (v) document.addEventListener('keydown', onFullKey)
  else document.removeEventListener('keydown', onFullKey)
}, { immediate: true })
onBeforeUnmount(() => document.removeEventListener('keydown', onFullKey))

const HEADER = 44

// A press anywhere in the 44px header (its buttons and tabs too) becomes a drag once the pointer moves;
// a click without movement still reaches the button. Text fields are left alone.
function onPointerDown(e: PointerEvent) {
  if (e.button !== 0) return
  const y = e.clientY - (e.currentTarget as HTMLElement).getBoundingClientRect().top
  const field = (e.target as HTMLElement).closest('input, textarea, select, [contenteditable]')
  if (y <= HEADER && !field) startDrag(props.pane, e)
}

const region = computed(() => drag.value?.target === props.pane ? drag.value.region : null)

// The highlighted part of the target: the half the dragged pane will take, or the whole pane for a swap.
const highlight = computed(() => ({
  left: 'inset-y-0 left-0 w-1/2',
  right: 'inset-y-0 right-0 w-1/2',
  top: 'inset-x-0 top-0 h-1/2',
  bottom: 'inset-x-0 bottom-0 h-1/2',
  center: 'inset-0'
}[region.value ?? 'center']))
</script>

<template>
  <div
    class="min-h-0 min-w-0 overflow-hidden bg-default"
    :class="[full ? 'fixed inset-0 z-40 flex flex-col' : 'relative size-full', drag?.src === pane && 'opacity-50']"
    :data-pane="pane"
    @pointerdown="full ? undefined : onPointerDown($event)"
  >
    <!-- Full screen: this bar goes back and, in the desktop app, drags the window, clear of its window buttons. -->
    <header
      v-if="full"
      data-tauri-drag-region
      class="flex h-11 shrink-0 items-center gap-1.5 border-b border-default pr-4"
      :class="desktop ? 'pl-[92px]' : 'pl-2'"
    >
      <HIconBtn
        icon="arrow-left"
        :label="tt('Back')"
        kbd="esc"
        @click="leave(session.meta.id)"
      />
      <h2
        data-tauri-drag-region
        class="m-0 min-w-0 flex-1 self-stretch truncate font-display text-[13.5px] leading-[44px] font-semibold text-strong"
      >
        {{ TITLES[pane] }}
      </h2>
    </header>
    <component
      :is="component"
      :class="full && 'min-h-0 flex-1'"
      :session="session"
      :menu="menu"
    />
    <div
      v-if="region"
      class="pointer-events-none absolute z-50 border border-primary bg-primary/15 transition-all duration-100"
      :class="highlight"
    />
  </div>
</template>
