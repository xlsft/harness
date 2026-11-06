<script setup lang="ts">
// User message (User.dc.html): right-aligned bubble. Slash commands and @mentions are highlighted,
// a leading "> quote" line renders as a reply-to fragment, `queued` uses the dashed waiting style (a click takes it
// back into the composer: `edit`), `pending` (handed to the client, not read yet) a plain muted one.
import type { Attachment } from '~/lib/session/types'
import { imageSrc } from '~/lib/session/objects'
import { FROM_VISUAL } from '~/lib/visual'
import { RAN_PREFIX } from '~/stores/sessions'

const props = withDefaults(defineProps<{
  text: string
  attachments?: Attachment[]
  queued?: boolean
  pending?: boolean
}>(), { attachments: () => [], queued: false, pending: false })
const emit = defineEmits<{ edit: [] }>()

// Images show as images; a message still waiting is not in the viewer's list yet, so it opens nothing.
const images = computed(() => props.attachments.flatMap((a) => {
  const src = a.kind === 'image' ? imageSrc({ url: a.url ?? '', object: a.object }) : ''
  return src ? [{ src, name: a.name }] : []
}))
const others = computed(() => props.attachments.filter(a => a.kind !== 'image' || !imageSrc({ url: a.url ?? '', object: a.object })))
const { open: openImage } = useImageViewer()

// Review comments from the diff pane get their own card.
const review = computed(() => parseReview(props.text))

// Sent by a code block's Run once its command finished: a quiet line, the output is in the block above.
const ran = computed(() => props.text.startsWith(RAN_PREFIX) ? props.text.slice(RAN_PREFIX.length) : '')

// The marker the client records when the user stops a turn: plain text, not a bubble.
const interrupted = computed(() => /^\[Request interrupted by user[^\]]*\]$/.test(props.text.trim()))

// Sent by a button inside a visual (harness.send): a chip instead of the "[from visual <id>]" prefix.
const fromVisual = computed(() => props.text.match(FROM_VISUAL)?.[1] ?? '')

// The text as Markdown-style quotes ("> " lines, one block per run) and the replies between them, each reply split
// into plain text, a leading /command and @mentions.
type Part = { t: string, kind: 'text' | 'command' | 'mention' }
function partsOf(body: string): Part[] {
  const parts: Part[] = []
  const re = /(^\/[\w-]+)|(@[\w./-]+)/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(body))) {
    if (m.index > last) parts.push({ t: body.slice(last, m.index), kind: 'text' })
    parts.push({ t: m[0], kind: m[1] ? 'command' : 'mention' })
    last = m.index + m[0].length
  }
  if (last < body.length) parts.push({ t: body.slice(last), kind: 'text' })
  return parts
}
const parsed = computed(() => {
  const text = fromVisual.value ? props.text.replace(FROM_VISUAL, '') : props.text
  const blocks: ({ kind: 'quote', text: string } | { kind: 'body', parts: Part[] })[] = []
  let quote: string[] = []
  let body: string[] = []
  const flush = () => {
    if (quote.length) blocks.push({ kind: 'quote', text: quote.join('\n') })
    const b = body.join('\n').trim()
    if (b) blocks.push({ kind: 'body', parts: partsOf(b) })
    quote = []
    body = []
  }
  for (const line of text.split('\n')) {
    const q = line.match(/^>\s?(.*)$/)
    if (q) {
      if (body.join('').trim()) flush()
      else body = []
      quote.push(q[1]!)
    } else {
      if (quote.length && !body.length) {
        blocks.push({ kind: 'quote', text: quote.join('\n') })
        quote = []
      }
      body.push(line)
    }
  }
  flush()
  return { blocks, rich: blocks.some(b => b.kind === 'body' && b.parts.some(p => p.kind !== 'text')) }
})
</script>

<template>
  <div class="flex flex-col items-end gap-1.5">
    <div
      v-if="images.length"
      class="flex max-w-[78%] flex-wrap justify-end gap-1.5"
    >
      <component
        :is="queued || pending ? 'span' : 'button'"
        v-for="(img, i) in images"
        :key="i"
        :type="queued || pending ? undefined : 'button'"
        :aria-label="queued || pending ? undefined : tt('Open {0}', [img.name])"
        @click="!queued && !pending && openImage(img.src)"
      >
        <img
          :src="img.src"
          :alt="img.name"
          class="max-h-[240px] max-w-full border border-default object-contain"
        >
      </component>
    </div>
    <MediaAttachment
      v-if="others.length"
      :files="others"
      side="right"
    />
    <button
      v-if="queued"
      type="button"
      :title="tt('Edit: back to the composer')"
      class="flex max-w-[78%] cursor-text flex-col gap-[7px] self-end border border-dashed border-accented px-3 py-[9px] text-left leading-[1.55] text-toned hover:border-accent"
      @click="emit('edit')"
    >
      <span
        v-if="!parsed.blocks.length"
        class="text-muted"
      >{{ tt('Files only') }}</span>
      <template
        v-for="(b, bi) in parsed.blocks"
        :key="bi"
      >
        <span
          v-if="b.kind === 'quote'"
          class="border-l-2 border-accented pl-2.5 text-[12px] whitespace-pre-wrap text-muted"
        >{{ b.text }}</span>
        <span
          v-else
          class="whitespace-pre-wrap"
        ><template
          v-for="(p, i) in b.parts"
          :key="i"
        ><span
          v-if="p.kind === 'command'"
          class="border border-accent px-[5px] text-accent"
        >{{ p.t }}</span><span
          v-else-if="p.kind === 'mention'"
          class="bg-accent/15 px-1 text-accent"
        >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
      </template>
    </button>
    <template v-else-if="!parsed.blocks.length && attachments.length" />
    <div
      v-else-if="pending"
      class="flex max-w-[78%] flex-col gap-[7px] self-end border border-default px-3 py-[9px] leading-[1.55] text-toned"
    >
      <template
        v-for="(b, bi) in parsed.blocks"
        :key="bi"
      >
        <span
          v-if="b.kind === 'quote'"
          class="border-l-2 border-accented pl-2.5 text-[12px] whitespace-pre-wrap text-muted"
        >{{ b.text }}</span>
        <span
          v-else
          class="whitespace-pre-wrap"
        ><template
          v-for="(p, i) in b.parts"
          :key="i"
        ><span
          v-if="p.kind === 'command'"
          class="border border-accent px-[5px] text-accent"
        >{{ p.t }}</span><span
          v-else-if="p.kind === 'mention'"
          class="bg-accent/15 px-1 text-accent"
        >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
      </template>
    </div>
    <span
      v-else-if="ran"
      class="inline-flex max-w-[78%] items-center gap-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="play"
        :size="12"
      />{{ tt('Ran') }} <code class="truncate font-mono text-toned">{{ ran.split('\n')[0] }}</code>
    </span>
    <span
      v-else-if="interrupted"
      class="inline-flex items-center gap-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="interrupt"
        :size="13"
      />{{ tt('Stopped') }}
    </span>
    <ReviewMessage
      v-else-if="review"
      :items="review"
    />
    <div
      v-else
      class="flex max-w-[78%] flex-col gap-[7px] self-end border border-default bg-hover px-3 py-[9px] text-strong"
      :class="parsed.rich ? 'leading-[1.7]' : 'leading-[1.55]'"
    >
      <span
        v-if="fromVisual"
        class="self-start border border-accented px-[5px] text-[11.5px] text-muted"
      >{{ tt('from visual') }}</span>
      <template
        v-for="(b, bi) in parsed.blocks"
        :key="bi"
      >
        <span
          v-if="b.kind === 'quote'"
          class="border-l-2 border-accented pl-2.5 text-[12px] whitespace-pre-wrap text-muted"
        >{{ b.text }}</span>
        <span
          v-else
          class="whitespace-pre-wrap"
        ><template
          v-for="(p, i) in b.parts"
          :key="i"
        ><span
          v-if="p.kind === 'command'"
          class="border border-accent px-[5px] text-accent"
        >{{ p.t }}</span><span
          v-else-if="p.kind === 'mention'"
          class="bg-accent/15 px-1 text-accent"
        >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
      </template>
    </div>
  </div>
</template>
