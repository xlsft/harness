<script setup lang="ts">
// Side pane of a SendUserFile card: renders an image, SVG, PDF, Markdown or HTML file. HTML runs like a Visual, in a
// frame sandboxed with scripts only (null origin) under the same CSP; an SVG is drawn as an image, so its scripts do not
// run.
import type { SessionState } from '~/lib/session/types'
import { sentFileUrl, type SentFileBytes } from '~/composables/useSentFiles'
import { visualDocument } from '~/lib/visual'
import { newUuid } from '~/stores/sessions'

const props = defineProps<{ session: SessionState, menu: string }>()
const { closePane } = usePanes()
const { open, load, download } = useSentFiles()
const file = computed(() => open.value[props.session.meta.id] ?? null)
const toast = useToast()

const content = ref<SentFileBytes | null>(null)
const failed = ref('')
const url = ref('')
const nonce = newUuid()
const text = computed(() => content.value ? new TextDecoder().decode(content.value.bytes) : '')
const srcdoc = computed(() => file.value?.kind === 'html' && text.value ? visualDocument(text.value, nonce) : '')

function clearUrl() {
  if (url.value) URL.revokeObjectURL(url.value)
  url.value = ''
}
onBeforeUnmount(clearUrl)

watch(file, async (f) => {
  clearUrl()
  content.value = null
  failed.value = ''
  if (!f) return
  try {
    const c = await load(props.session.meta.id, f)
    if (file.value !== f) return
    content.value = c
    if (f.kind === 'image' || f.kind === 'svg' || f.kind === 'pdf') url.value = sentFileUrl(f, c.bytes)
  } catch (e) {
    if (file.value !== f) return
    const device = props.session.meta.remote ? props.session.meta.deviceName || 'the machine that runs this session' : ''
    failed.value = device ? `This file is too large to reach this device. It can be viewed on ${device}.` : e instanceof Error ? e.message : String(e)
  }
}, { immediate: true })

async function save() {
  if (!file.value) return
  try {
    await download(props.session.meta.id, file.value)
  } catch {
    toast.add({ title: 'Could not download the file', color: 'error' })
  }
}
</script>

<template>
  <section
    aria-label="File"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="min-w-0 flex-1 truncate font-display text-[13px] text-strong">{{ file?.name ?? 'File' }}</span>
      <PaneBtn
        v-if="file"
        icon="download"
        label="Download"
        @click="save"
      />
      <PaneBtn
        icon="close"
        label="Close"
        @click="closePane('sentfile')"
      />
    </div>
    <p
      v-if="content?.changed"
      class="m-0 border-b border-line px-3.5 py-1.5 text-[12px] text-muted"
    >
      The file changed after it was sent; this is how it is now.
    </p>

    <div
      v-if="!file || failed"
      class="flex flex-1 items-center justify-center p-6 text-center text-[12.5px] text-muted"
    >
      {{ failed || 'No file open' }}
    </div>
    <div
      v-else-if="!content"
      class="flex flex-1 items-center justify-center text-[12.5px] text-muted"
    >
      Loading
    </div>
    <div
      v-else-if="file.kind === 'image' || file.kind === 'svg'"
      class="flex min-h-0 flex-1 items-start justify-center overflow-auto p-4"
    >
      <img
        v-if="url"
        :src="url"
        :alt="file.name"
        class="max-w-full"
      >
    </div>
    <iframe
      v-else-if="file.kind === 'pdf' && url"
      :src="url"
      :title="file.name"
      class="min-h-0 w-full flex-1 border-0"
    />
    <iframe
      v-else-if="file.kind === 'html'"
      :srcdoc="srcdoc"
      :title="file.name"
      sandbox="allow-scripts"
      class="min-h-0 w-full flex-1 border-0"
    />
    <div
      v-else-if="file.kind === 'markdown'"
      class="min-h-0 flex-1 overflow-auto px-4 py-3"
    >
      <MarkdownBlock :text="text" />
    </div>
    <div
      v-else
      class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center text-[12.5px] text-muted"
    >
      This type of file cannot be shown here.
      <HBtn @click="save">
        Download
      </HBtn>
    </div>
  </section>
</template>
