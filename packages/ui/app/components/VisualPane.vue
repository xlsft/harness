<script setup lang="ts">
// Side pane of a Visual card: the model's HTML at the pane's size, in a frame sandboxed with scripts only. harness.send()
// inside it posts a "[from visual <id>]" message to the session, as in the card.
import type { SessionState } from '~/lib/session/types'
import { visualDocument, type VisualMessage } from '~/lib/visual'
import { newUuid } from '~/stores/sessions'

const props = defineProps<{ session: SessionState, menu: string }>()
const { closePane } = usePanes()
const { open } = useVisualPane()
const store = useSessionStore()
const visual = computed(() => open.value[props.session.meta.id] ?? null)

const nonce = ref(newUuid())
// Rebuilt when the theme changes, so the visual's CSS variables follow it.
const { theme } = useAppearance()
const srcdoc = computed(() => visual.value?.html && theme.value ? visualDocument(visual.value.html, nonce.value) : '')
const frame = ref<HTMLIFrameElement | null>(null)

function onMessage(e: MessageEvent) {
  const d = e.data as VisualMessage & { harnessVisual?: string }
  if (!d || d.harnessVisual !== nonce.value || e.source !== frame.value?.contentWindow) return
  if (typeof d.send === 'string' && d.send.trim()) store.send(props.session.meta.id, `[from visual ${visual.value?.id ?? ''}] ${d.send.trim()}`)
}
onMounted(() => window.addEventListener('message', onMessage))
onBeforeUnmount(() => window.removeEventListener('message', onMessage))
</script>

<template>
  <section
    :aria-label="tt('Visual')"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="min-w-0 flex-1 truncate font-display text-[13px] text-strong">{{ visual?.title ?? tt('Visual') }}</span>
      <PaneBtn
        icon="reload"
        :label="tt('Replay')"
        @click="nonce = newUuid()"
      />
      <PaneFullBtn />
      <PaneBtn
        icon="close"
        :label="tt('Close')"
        @click="closePane('visual')"
      />
    </div>
    <iframe
      v-if="srcdoc"
      ref="frame"
      :key="nonce"
      :srcdoc="srcdoc"
      :title="visual?.title"
      sandbox="allow-scripts"
      class="min-h-0 w-full flex-1 border-0"
    />
    <div
      v-else
      class="flex flex-1 items-center justify-center p-6 text-center text-[12.5px] text-muted"
    >
      {{ tt('No visual open') }}
    </div>
  </section>
</template>
