<script setup lang="ts">
// User message (User.dc.html): right-aligned bubble. Slash commands and @mentions are highlighted,
// a leading "> quote" line renders as a reply-to fragment, `queued` uses the dashed waiting style (a click takes it
// back into the composer: `edit`), `pending` (handed to the client, not read yet) a plain muted one.
import type { Attachment } from '~/lib/session/types'
import { FROM_VISUAL } from '~/lib/visual'
import { RAN_PREFIX } from '~/stores/sessions'

const props = withDefaults(defineProps<{
  text: string
  attachments?: Attachment[]
  queued?: boolean
  pending?: boolean
}>(), { attachments: () => [], queued: false, pending: false })
const emit = defineEmits<{ edit: [] }>()

// Review comments from the diff pane get their own card.
const review = computed(() => parseReview(props.text))

// Sent by a code block's Run once its command finished: a quiet line, the output is in the block above.
const ran = computed(() => props.text.startsWith(RAN_PREFIX) ? props.text.slice(RAN_PREFIX.length) : '')

// The marker the client records when the user stops a turn: plain text, not a bubble.
const interrupted = computed(() => /^\[Request interrupted by user[^\]]*\]$/.test(props.text.trim()))

// Sent by a button inside a visual (harness.send): a chip instead of the "[from visual <id>]" prefix.
const fromVisual = computed(() => props.text.match(FROM_VISUAL)?.[1] ?? '')

const parsed = computed(() => {
  const text = fromVisual.value ? props.text.replace(FROM_VISUAL, '') : props.text
  const lines = text.split('\n')
  const quote = lines[0]?.startsWith('> ') ? lines[0].slice(2) : ''
  const body = quote ? lines.slice(1).join('\n').trim() : text
  const parts: { t: string, kind: 'text' | 'command' | 'mention' }[] = []
  const re = /(^\/[\w-]+)|(@[\w./-]+)/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(body))) {
    if (m.index > last) parts.push({ t: body.slice(last, m.index), kind: 'text' })
    parts.push({ t: m[0], kind: m[1] ? 'command' : 'mention' })
    last = m.index + m[0].length
  }
  if (last < body.length) parts.push({ t: body.slice(last), kind: 'text' })
  return { quote, parts, rich: parts.some(p => p.kind !== 'text') }
})
</script>

<template>
  <div class="flex flex-col items-end gap-1.5">
    <MediaAttachment
      v-if="attachments.length"
      :files="attachments"
      side="right"
    />
    <button
      v-if="queued"
      type="button"
      title="Edit: back to the composer"
      class="flex max-w-[78%] cursor-text flex-col gap-[7px] self-end border border-dashed border-accented px-3 py-[9px] text-left leading-[1.55] text-toned hover:border-accent"
      @click="emit('edit')"
    >
      <span
        v-if="parsed.quote"
        class="border-l-2 border-accented pl-2.5 text-[12px] text-muted"
      >{{ parsed.quote }}</span>
      <span class="whitespace-pre-wrap"><template
        v-for="(p, i) in parsed.parts"
        :key="i"
      ><span
        v-if="p.kind === 'command'"
        class="border border-accent px-[5px] text-accent"
      >{{ p.t }}</span><span
        v-else-if="p.kind === 'mention'"
        class="bg-accent/15 px-1 text-accent"
      >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
    </button>
    <div
      v-else-if="pending"
      class="flex max-w-[78%] flex-col gap-[7px] self-end border border-default px-3 py-[9px] leading-[1.55] text-toned"
    >
      <span
        v-if="parsed.quote"
        class="border-l-2 border-accented pl-2.5 text-[12px] text-muted"
      >{{ parsed.quote }}</span>
      <span class="whitespace-pre-wrap"><template
        v-for="(p, i) in parsed.parts"
        :key="i"
      ><span
        v-if="p.kind === 'command'"
        class="border border-accent px-[5px] text-accent"
      >{{ p.t }}</span><span
        v-else-if="p.kind === 'mention'"
        class="bg-accent/15 px-1 text-accent"
      >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
    </div>
    <span
      v-else-if="ran"
      class="inline-flex max-w-[78%] items-center gap-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="play"
        :size="12"
      />Ran <code class="truncate font-mono text-toned">{{ ran.split('\n')[0] }}</code>
    </span>
    <span
      v-else-if="interrupted"
      class="inline-flex items-center gap-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="interrupt"
        :size="13"
      />Stopped
    </span>
    <ReviewMessage
      v-else-if="review"
      :items="review"
    />
    <div
      v-else
      class="flex max-w-[78%] flex-col gap-[7px] self-end border border-default bg-hover px-3 py-[9px] whitespace-pre-wrap text-strong"
      :class="parsed.rich ? 'leading-[1.7]' : 'leading-[1.55]'"
    >
      <span
        v-if="parsed.quote"
        class="border-l-2 border-accented pl-2.5 text-[12px] text-muted"
      >{{ parsed.quote }}</span>
      <span><span
        v-if="fromVisual"
        class="mr-1.5 border border-accented px-[5px] text-[11.5px] text-muted"
      >from visual</span><template
        v-for="(p, i) in parsed.parts"
        :key="i"
      ><span
        v-if="p.kind === 'command'"
        class="border border-accent px-[5px] text-accent"
      >{{ p.t }}</span><span
        v-else-if="p.kind === 'mention'"
        class="bg-accent/15 px-1 text-accent"
      >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
    </div>
  </div>
</template>
