<script setup lang="ts">
// Visual tool card: the model's HTML in a sandboxed frame that fits its content (up to 80% of the window),
// a bar with the title, versions (calls with the same id), replay, full screen and a menu (source, copy,
// download). harness.send() inside the visual posts a "[from visual <id>]" message to the session.
import type { ToolItem } from '~/lib/session/types'
import { visualDocument, type VisualMessage } from '~/lib/visual'
import { newUuid } from '~/stores/sessions'

const props = defineProps<{ versions: ToolItem[], sessionId: string }>()
const store = useSessionStore()
const toast = useToast()

const index = ref(props.versions.length - 1)
watch(() => props.versions.length, (n) => {
  index.value = n - 1
})
const t = computed(() => props.versions[index.value] ?? props.versions[props.versions.length - 1]!)
const id = computed(() => String(t.value.input.id ?? ''))
const title = computed(() => String(t.value.input.title ?? '') || 'Visual')
const html = computed(() => String(t.value.input.html ?? ''))
const drawing = computed(() => t.value.status === 'running' && !html.value)

const nonce = ref(newUuid())
const srcdoc = computed(() => html.value ? visualDocument(html.value, nonce.value) : '')
const frame = ref<HTMLIFrameElement | null>(null)
const full = ref(false)
const fullFrame = ref<HTMLIFrameElement | null>(null)
const height = ref(160)
const menu = ref(false)
const source = ref(false)

function replay() {
  nonce.value = newUuid()
}

function onMessage(e: MessageEvent) {
  const d = e.data as VisualMessage & { harnessVisual?: string }
  if (!d || d.harnessVisual !== nonce.value) return
  const fromCard = e.source === frame.value?.contentWindow
  if (!fromCard && e.source !== fullFrame.value?.contentWindow) return
  if (typeof d.height === 'number' && fromCard) height.value = Math.max(40, d.height)
  if (typeof d.send === 'string' && d.send.trim()) store.send(props.sessionId, `[from visual ${id.value}] ${d.send.trim()}`)
}
onMounted(() => window.addEventListener('message', onMessage))
onBeforeUnmount(() => window.removeEventListener('message', onMessage))

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') full.value = false
}
watch(full, (v) => {
  if (v) document.addEventListener('keydown', onKey)
  else document.removeEventListener('keydown', onKey)
})
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))

async function copy() {
  menu.value = false
  try {
    await navigator.clipboard.writeText(html.value)
    toast.add({ title: 'HTML copied', color: 'neutral' })
  } catch {
    toast.add({ title: 'Could not copy', color: 'error' })
  }
}

function download() {
  menu.value = false
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([html.value], { type: 'text/html' }))
  a.download = `${id.value || 'visual'}.html`
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}
</script>

<template>
  <div class="border border-default bg-surface">
    <div class="flex h-9 items-center gap-1 border-b border-default pr-1.5 pl-3 text-[12px] text-muted">
      <HIcon
        name="design"
        :size="13"
        class="mr-1"
      />
      <span class="min-w-0 flex-1 truncate text-white">{{ title }}</span>
      <span
        v-if="versions.length > 1"
        class="flex items-center"
      >
        <button
          type="button"
          aria-label="Previous version"
          class="flex size-6 items-center justify-center hover:text-white disabled:opacity-40"
          :disabled="index === 0"
          @click="index--"
        >
          <HIcon
            name="arrow-left"
            :size="12"
          />
        </button>
        <span class="tabular-nums">v{{ index + 1 }}/{{ versions.length }}</span>
        <button
          type="button"
          aria-label="Next version"
          class="flex size-6 items-center justify-center hover:text-white disabled:opacity-40"
          :disabled="index === versions.length - 1"
          @click="index++"
        >
          <HIcon
            name="arrow-right"
            :size="12"
          />
        </button>
      </span>
      <HIconBtn
        icon="reload"
        label="Replay"
        :size="26"
        :icon-size="13"
        @click="replay"
      />
      <HIconBtn
        icon="full"
        label="Full screen"
        :size="26"
        :icon-size="13"
        @click="full = true"
      />
      <UPopover
        v-model:open="menu"
        :content="{ side: 'bottom', align: 'end', sideOffset: 4 }"
      >
        <HIconBtn
          icon="more"
          label="More"
          :size="26"
          :icon-size="13"
          :pressed="menu"
        />
        <template #content>
          <div
            role="menu"
            :aria-label="title"
            class="w-[200px] border border-accented bg-hover py-1 text-[12.5px]"
          >
            <PaneMenuItem
              icon="code"
              @click="source = !source; menu = false"
            >
              {{ source ? 'Hide source' : 'View source' }}
            </PaneMenuItem>
            <PaneMenuItem
              icon="copy"
              @click="copy"
            >
              Copy HTML
            </PaneMenuItem>
            <PaneMenuItem
              icon="download"
              @click="download"
            >
              Download .html
            </PaneMenuItem>
          </div>
        </template>
      </UPopover>
    </div>
    <div
      v-if="drawing"
      class="flex h-[120px] items-center justify-center gap-2 text-[12px] text-muted"
    >
      <HIcon
        name="loading"
        :size="13"
        spin
      />Drawing…
    </div>
    <iframe
      v-else
      ref="frame"
      :key="nonce + index"
      :srcdoc="srcdoc"
      sandbox="allow-scripts"
      :title="title"
      class="block max-h-[80vh] w-full border-0"
      :style="{ height: `${height}px` }"
    />
    <pre
      v-if="source"
      class="m-0 max-h-[260px] overflow-auto border-t border-default px-3 py-2.5 text-[11.5px] leading-[1.55] text-toned"
    >{{ html }}</pre>
    <Teleport to="body">
      <div
        v-if="full"
        class="fixed inset-0 z-50 flex flex-col bg-ground"
      >
        <div class="flex h-11 shrink-0 items-center gap-2 border-b border-default pr-2 pl-4 text-[12.5px]">
          <span class="min-w-0 flex-1 truncate text-white">{{ title }}</span>
          <HIconBtn
            icon="close"
            label="Close"
            kbd="esc"
            @click="full = false"
          />
        </div>
        <iframe
          ref="fullFrame"
          :key="'full' + nonce + index"
          :srcdoc="srcdoc"
          sandbox="allow-scripts"
          :title="title"
          class="block w-full flex-1 border-0"
        />
      </div>
    </Teleport>
  </div>
</template>
