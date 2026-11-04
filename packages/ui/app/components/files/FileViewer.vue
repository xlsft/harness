<script setup lang="ts">
// The selected file of the files pane: code and text with highlighting (utils/highlight.ts) and line numbers,
// markdown rendered or as source, images (SVG as an image, or its source), and for binary or too large files
// their size with ways to open them elsewhere. Lines from Show are highlighted and scrolled to; a re-read of
// the same file keeps the scroll position.
import { Virtualizer } from 'virtua/vue'
import type { FileContent } from '~/lib/files/types'
import { FILE_LIMITS } from '~/lib/files/types'
import { fileKind, formatSize, languageOf } from '~/lib/files/tree'
import { highlightLine } from '~/utils/highlight'

const props = defineProps<{
  path: string
  content: FileContent | null
  loading: boolean
  error: string
  wrap: boolean
  tabSize: number
  // Show's lines (1-based, inclusive), and a counter that changes for every request to scroll to them again.
  lines: { start: number, end: number } | null
  scrollRequest: number
  desktop: boolean
}>()
const source = defineModel<boolean>('source', { default: false })
const emit = defineEmits<{ 'copy-path': [], 'open-app': [] }>()

// Highlighting stops here (plain text beyond), line rows stop at MAX_ROWS (one plain block beyond).
const MAX_HIGHLIGHT = 5000
const MAX_ROWS = 20000

const kind = computed(() => fileKind(props.path))
const lang = computed(() => languageOf(props.path))

const view = computed<'loading' | 'error' | 'none' | 'image' | 'markdown' | 'code' | 'plain' | 'binary'>(() => {
  const c = props.content
  if (props.error) return 'error'
  if (!c) return props.loading ? 'loading' : 'none'
  if (c.data && c.mime.startsWith('image/')) return 'image'
  if (kind.value === 'svg' && !c.binary && !source.value) return 'image'
  if (c.binary || c.tooLarge) return 'binary'
  if (kind.value === 'md' && !source.value) return 'markdown'
  return lineCount.value > MAX_ROWS ? 'plain' : 'code'
})

const textLines = computed(() => (props.content?.text ?? '').replace(/\n$/, '').split('\n'))
const lineCount = computed(() => textLines.value.length)
// A line is highlighted when it first comes on screen, and remembered for as long as the text stays.
const rows = computed(() => {
  const hl = lineCount.value <= MAX_HIGHLIGHT
  const language = lang.value.lang
  return textLines.value.map((line) => {
    let spans: ReturnType<typeof highlightLine> | null = null
    return { spans: () => spans ??= hl ? highlightLine(line, language) : [{ t: line, c: 'var(--color-text)' }] }
  })
})
const lineList = ref<InstanceType<typeof Virtualizer> | null>(null)

// An object URL for an image's bytes or an SVG's text, revoked when it changes.
const imageUrl = ref('')
watch(() => [props.content, view.value] as const, ([c, v]) => {
  if (imageUrl.value) URL.revokeObjectURL(imageUrl.value)
  imageUrl.value = ''
  if (!c || v !== 'image') return
  const blob = c.data ? new Blob([c.data as BlobPart], { type: c.mime }) : new Blob([c.text], { type: 'image/svg+xml' })
  imageUrl.value = URL.createObjectURL(blob)
}, { immediate: true })
onBeforeUnmount(() => imageUrl.value && URL.revokeObjectURL(imageUrl.value))
const imageSize = ref('')

const scroller = ref<HTMLElement | null>(null)

// Same file re-read (it changed on disk): keep where the reader was.
let kept: { path: string, top: number, left: number } | null = null
watch(() => props.content, () => {
  const el = scroller.value
  kept = el ? { path: props.path, top: el.scrollTop, left: el.scrollLeft } : null
}, { flush: 'pre' })
watch(() => props.content, async () => {
  await nextTick()
  const el = scroller.value
  if (el && kept && kept.path === props.path) {
    el.scrollTop = kept.top
    el.scrollLeft = kept.left
  }
}, { flush: 'post' })

// Show: scroll to its lines once they are rendered.
watch(() => [props.scrollRequest, view.value] as const, async () => {
  if (!props.lines || view.value !== 'code') return
  await nextTick()
  lineList.value?.scrollToIndex(props.lines.start - 1, { align: 'center' })
}, { immediate: true })

const isShown = (n: number) => !!props.lines && n >= props.lines.start && n <= props.lines.end
const gutter = computed(() => `${Math.max(2, String(lineCount.value).length) + 2}ch`)
</script>

<template>
  <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <div
      v-if="content?.truncated && view === 'code'"
      role="status"
      class="shrink-0 border-b border-line bg-surface px-3.5 py-1.5 text-[11.5px] text-muted"
    >
      Showing the first {{ formatSize(FILE_LIMITS.text) }} of {{ formatSize(content.size) }}.
    </div>

    <div
      v-if="view === 'loading'"
      class="flex flex-1 items-center justify-center text-muted"
    >
      <HIcon
        name="loading"
        spin
        :size="18"
      />
    </div>

    <div
      v-else-if="view === 'error'"
      class="flex flex-1 flex-col items-center justify-center gap-2 p-5 text-center"
    >
      <HIcon
        name="warn"
        :size="22"
        class="text-warn"
      />
      <span class="text-[13px] text-strong">Could not open this file</span>
      <span class="max-w-[420px] text-[12px] break-words text-muted">{{ error }}</span>
    </div>

    <div
      v-else-if="view === 'image'"
      ref="scroller"
      class="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 overflow-auto bg-surface p-5"
    >
      <img
        v-if="imageUrl"
        :src="imageUrl"
        :alt="path"
        class="max-h-full max-w-full object-contain [image-rendering:auto]"
        style="background: repeating-conic-gradient(var(--color-hover) 0 25%, transparent 0 50%) 0 0 / 16px 16px"
        @load="imageSize = `${($event.target as HTMLImageElement).naturalWidth} × ${($event.target as HTMLImageElement).naturalHeight}`"
      >
      <span class="text-[11.5px] text-muted">{{ imageSize }}{{ imageSize && content ? ' · ' : '' }}{{ content ? formatSize(content.size) : '' }}</span>
    </div>

    <div
      v-else-if="view === 'binary' && content"
      class="flex flex-1 flex-col items-center justify-center gap-2.5 p-5 text-center"
    >
      <HIcon
        name="file"
        :size="26"
        class="text-muted"
      />
      <span class="text-[13px] text-strong">No preview for this file</span>
      <span class="text-[12px] text-muted">
        {{ formatSize(content.size) }}{{ content.tooLarge ? ' · too large to preview' : content.mime ? ` · ${content.mime}` : '' }}
      </span>
      <div class="flex flex-wrap justify-center gap-1.5 pt-1.5">
        <button
          v-if="desktop"
          type="button"
          class="border border-line bg-hover px-2.5 py-[5px] text-[12px] text-strong hover:bg-raised"
          @click="emit('open-app')"
        >
          Open in default app
        </button>
        <button
          type="button"
          class="border border-line bg-hover px-2.5 py-[5px] text-[12px] text-strong hover:bg-raised"
          @click="emit('copy-path')"
        >
          Copy path
        </button>
      </div>
    </div>

    <div
      v-else-if="view === 'markdown' && content"
      ref="scroller"
      class="min-h-0 flex-1 overflow-auto px-[18px] py-4"
    >
      <MarkdownBlock :text="content.text" />
    </div>

    <pre
      v-else-if="view === 'plain' && content"
      ref="scroller"
      class="m-0 min-h-0 flex-1 overflow-auto px-3.5 py-2 font-mono text-[12px] leading-[1.65] text-toned"
      :class="wrap ? 'break-words whitespace-pre-wrap' : 'whitespace-pre'"
      :style="{ tabSize }"
    >{{ content.text }}</pre>

    <div
      v-else-if="view === 'code' && content"
      ref="scroller"
      class="min-h-0 flex-1 overflow-auto py-2 font-mono text-[12px] leading-[1.65]"
      :aria-label="`${path}, ${lang.label}`"
    >
      <Virtualizer
        ref="lineList"
        v-slot="{ item: l, index: i }"
        :data="rows"
        :start-margin="8"
      >
        <div
          :key="i"
          class="flex min-h-[1.65em]"
          :class="[isShown(i + 1) && 'bg-accent/15', !wrap && 'w-max min-w-full']"
          :data-line="i + 1"
        >
          <span
            class="shrink-0 pr-3 text-right select-none"
            :class="isShown(i + 1) ? 'text-strong' : 'text-dimmed'"
            :style="{ width: gutter }"
          >{{ i + 1 }}</span>
          <span
            class="min-w-0 flex-1 pr-2.5"
            :class="wrap ? 'break-words whitespace-pre-wrap' : 'whitespace-pre'"
            :style="{ tabSize }"
          ><span
            v-for="(s, j) in l.spans()"
            :key="j"
            :style="{ color: s.c }"
          >{{ s.t }}</span></span>
        </div>
      </Virtualizer>
    </div>

    <div
      v-else
      class="flex flex-1 flex-col items-center justify-center gap-2.5 p-5 text-center"
    >
      <HIcon
        name="folder"
        :size="26"
        class="text-muted"
      />
      <span class="text-[13px] text-strong">No file open</span>
      <span class="text-[12px] text-muted">Pick a file in the tree</span>
    </div>
  </div>
</template>
