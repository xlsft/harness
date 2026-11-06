<script setup lang="ts">
// Chat events and notices (Event.dc.html): hook output, provider error with retry, context compaction,
// interruption, turn usage footer, and plain info / warning notices.
import type { CompactItem, HookItem, NoticeItem, TurnItem } from '~/lib/session/types'

const props = defineProps<{
  notice?: NoticeItem
  hook?: HookItem
  compact?: CompactItem
  turn?: TurnItem
  // The turn footer's Read aloud is on (this turn's answer is being spoken).
  speaking?: boolean
  // Its audio is being made (before any sound).
  speechLoading?: boolean
  // The session's engine cannot fork.
  noFork?: boolean
}>()
const summaryOpen = ref(false)
const emit = defineEmits<{ retry: [], switchModel: [], copy: [], fork: [], readAloud: [] }>()

// What the row says after the command: the hook's own words, else how it ended.
const hookResult = computed(() => {
  const h = props.hook
  if (!h) return ''
  if (h.output) return h.output
  switch (h.state) {
    case 'running': return h.async ? 'running on its own' : 'running'
    case 'blocked': return 'blocked'
    case 'error': return 'failed'
    case 'timed_out': return 'timed out'
    case 'canceled': return 'canceled'
  }
  return h.decision
})
const hookTone = computed(() => {
  switch (props.hook?.state) {
    case 'ok': return 'text-ok'
    case 'running': case 'canceled': return 'text-muted'
    case 'blocked': return 'text-warn'
  }
  return 'text-bad'
})

// Provider errors: "title|detail" (the detail line is optional).
const error = computed(() => {
  if (!props.notice || props.notice.level !== 'error') return null
  const [title, detail] = props.notice.text.split('|')
  return { title: title ?? '', detail: detail ?? '' }
})

const store = useSessionStore()
const usageLine = computed(() => {
  const t = props.turn
  if (!t) return ''
  const model = store.models.find(m => m.id === t.model)?.displayName || t.model
  return [model, `${formatTokens(t.usage.inputTokens + t.usage.cacheReadTokens)} in`, `${formatTokens(t.usage.outputTokens)} out`, formatCost(t.costUsd), formatDuration(t.durationMs)].filter(Boolean).join(' · ')
})
</script>

<template>
  <!-- hook output -->
  <div
    v-if="hook"
    class="flex flex-wrap items-center gap-[9px] border border-dashed border-default px-3 py-1.5 text-[12px] text-muted"
  >
    <HIcon
      name="hook"
      :size="14"
      class="text-warn"
    />
    {{ hook.event }}<template v-if="hook.toolName">
      ({{ hook.toolName }})
    </template>
    <span
      class="max-w-full truncate text-toned"
      :title="`${hook.command} (${hook.source} settings)`"
    >{{ hook.command }}</span>
    <span
      class="min-w-0 break-words whitespace-pre-wrap"
      :class="hookTone"
    >{{ hookResult }}</span>
    <span class="flex-1" />{{ hook.durationMs ? formatSeconds(hook.durationMs) : '' }}
  </div>

  <!-- provider error with retry -->
  <div
    v-else-if="error"
    class="flex flex-col gap-2 border border-l-2 border-default border-l-bad px-3 py-[9px] text-[12.5px]"
  >
    <span class="flex items-center gap-[9px]"><HIcon
      name="warn"
      class="text-bad"
    /><span class="text-strong">{{ error.title }}</span></span>
    <span
      v-if="error.detail"
      class="text-[12px] text-muted"
    >{{ error.detail }}</span>
    <div class="flex gap-2">
      <HBtn
        size="sm"
        @click="emit('retry')"
      >
        {{ tt('Retry now') }}
      </HBtn>
      <HBtn
        size="sm"
        @click="emit('switchModel')"
      >
        {{ tt('Switch model') }}
      </HBtn>
    </div>
  </div>

  <!-- a message or report from another session: never the user's word -->
  <div
    v-else-if="notice?.session"
    class="flex flex-col gap-1.5 text-[12px] text-muted"
    data-session-note
  >
    <span class="whitespace-pre-wrap">{{ notice.text }}</span>
    <SessionRow
      :id="notice.session.id"
      :name="notice.session.name"
      :title="notice.session.title"
    />
  </div>

  <!-- info / warning notice -->
  <div
    v-else-if="notice"
    class="flex items-center gap-2 text-[12px]"
    :class="notice.level === 'warning' ? 'text-warn' : 'text-muted'"
  >
    <HIcon
      :name="notice.level === 'warning' ? 'alert' : 'info'"
      :size="14"
    />{{ notice.text }}
  </div>

  <!-- compaction, with the summary the conversation continues from -->
  <div
    v-else-if="compact"
    class="flex flex-col gap-2"
  >
    <div class="flex items-center gap-3 text-[11.5px] text-muted">
      <span class="h-px flex-1 bg-line" />
      {{ tt('Context compacted{0} · {1} → {2} tokens', [compact.trigger === 'idle' ? ' while idle' : '', formatTokens(compact.tokensBefore), formatTokens(compact.tokensAfter)]) }}
      <button
        v-if="compact.summary"
        type="button"
        class="link"
        :aria-expanded="summaryOpen"
        @click="summaryOpen = !summaryOpen"
      >
        {{ summaryOpen ? 'hide summary' : 'summary' }}
      </button>
      <span class="h-px flex-1 bg-line" />
    </div>
    <div
      v-if="summaryOpen && compact.summary"
      class="max-h-80 overflow-y-auto border border-default bg-surface px-4 py-3 text-[13px]"
    >
      <MarkdownBlock :text="compact.summary" />
    </div>
  </div>

  <!-- interrupted turn -->
  <!-- interrupted turn: the model answers the Stop itself, in a message of its own -->
  <template v-else-if="turn && turn.result === 'interrupted'" />

  <!-- turn usage footer -->
  <div
    v-else-if="turn"
    class="flex flex-wrap items-center gap-1 text-[11px] text-muted"
  >
    <button
      type="button"
      :aria-label="tt('Copy')"
      class="flex size-[26px] items-center justify-center hover:text-highlighted"
      @click="emit('copy')"
    >
      <HIcon
        name="clipboard"
        :size="14"
      />
    </button>
    <button
      type="button"
      :aria-label="tt('Retry')"
      class="flex size-[26px] items-center justify-center hover:text-highlighted"
      @click="emit('retry')"
    >
      <HIcon
        name="retry"
        :size="14"
      />
    </button>
    <button
      v-if="!noFork"
      type="button"
      :aria-label="tt('Fork from here')"
      class="flex size-[26px] items-center justify-center hover:text-highlighted"
      @click="emit('fork')"
    >
      <HIcon
        name="branch"
        :size="14"
      />
    </button>
    <button
      type="button"
      :aria-label="speaking ? tt('Stop reading') : tt('Read aloud')"
      :title="speaking ? tt('Stop reading') : tt('Read aloud')"
      class="flex size-[26px] items-center justify-center hover:text-highlighted"
      :class="speaking && 'text-accent'"
      @click="emit('readAloud')"
    >
      <HIcon
        :name="speechLoading ? 'loading' : speaking ? 'stop' : 'speaker'"
        :spin="speechLoading"
        :size="14"
      />
    </button>
    <span class="flex-1" />
    <span
      v-if="turn.result === 'error' || turn.result === 'max_turns'"
      class="text-bad"
    >{{ turn.errorMessage || 'turn failed' }} ·</span>
    {{ usageLine }}
  </div>
</template>
