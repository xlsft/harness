<script setup lang="ts">
// User message (User.dc.html): right-aligned bubble. Slash commands and @mentions are highlighted,
// a leading "> quote" line renders as a reply-to fragment, `queued` uses the dashed waiting style.
import type { Attachment } from '~/lib/session/types'

const props = withDefaults(defineProps<{
  text: string
  attachments?: Attachment[]
  queued?: boolean
}>(), { attachments: () => [], queued: false })

const parsed = computed(() => {
  const lines = props.text.split('\n')
  const quote = lines[0]?.startsWith('> ') ? lines[0].slice(2) : ''
  const body = quote ? lines.slice(1).join('\n').trim() : props.text
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
    <div
      v-if="queued"
      class="flex max-w-[78%] flex-col gap-[5px] self-end border border-dashed border-accented px-3 py-[9px] leading-[1.55] text-toned"
    >
      <span class="text-[11px] text-muted">queued · sends after this turn</span>
      <span class="whitespace-pre-wrap">{{ text }}</span>
    </div>
    <div
      v-else
      class="flex max-w-[78%] flex-col gap-[7px] self-end border border-default bg-hover px-3 py-[9px] whitespace-pre-wrap text-white"
      :class="parsed.rich ? 'leading-[1.7]' : 'leading-[1.55]'"
    >
      <span
        v-if="parsed.quote"
        class="border-l-2 border-accented pl-2.5 text-[12px] text-muted"
      >{{ parsed.quote }}</span>
      <span><template
        v-for="(p, i) in parsed.parts"
        :key="i"
      ><span
        v-if="p.kind === 'command'"
        class="border border-accent px-[5px] text-accent"
      >{{ p.t }}</span><span
        v-else-if="p.kind === 'mention'"
        class="bg-[#2b7fff26] px-1 text-accent"
      >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
    </div>
  </div>
</template>
