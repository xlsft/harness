<script setup lang="ts">
// A user message written by the diff pane's review comments (utils/review.ts), drawn as a review card:
// per comment the file and line, the commented code line as a diff row, and the comment.
import type { ReviewItem } from '~/utils/review'

defineProps<{ items: ReviewItem[] }>()

const ROW = {
  '+': { bg: 'color-mix(in srgb, var(--color-ok) 12%, transparent)', sc: 'var(--color-ok)', s: '+' },
  '-': { bg: 'color-mix(in srgb, var(--color-bad) 12%, transparent)', sc: 'var(--color-bad)', s: '−' },
  ' ': { bg: 'transparent', sc: 'var(--color-muted)', s: ' ' }
}
</script>

<template>
  <div class="flex w-full max-w-[78%] flex-col self-end border border-default bg-hover text-[12px]">
    <div class="flex items-center gap-1.5 border-b border-default px-3 py-1.5 text-muted">
      <HIcon
        name="comment"
        :size="12"
        class="text-accent"
      />
      Review · {{ items.length }} comment{{ items.length === 1 ? '' : 's' }}
    </div>
    <div
      v-for="(c, i) in items"
      :key="i"
      class="flex flex-col"
      :class="i > 0 && 'border-t border-default'"
    >
      <div class="truncate px-3 pt-1.5 text-[11.5px] text-muted">
        {{ c.path }}:{{ c.line }}
      </div>
      <div
        class="mx-3 my-1 flex overflow-x-auto leading-[1.7] whitespace-pre"
        :style="{ background: ROW[c.sign].bg }"
      >
        <span class="w-[38px] shrink-0 pr-2.5 text-right text-dimmed">{{ c.line }}</span>
        <span
          class="w-3.5 shrink-0"
          :style="{ color: ROW[c.sign].sc }"
        >{{ ROW[c.sign].s }}</span>
        <span class="text-strong">{{ c.code }}</span>
      </div>
      <div class="px-3 pb-2 leading-[1.55] whitespace-pre-wrap text-strong">
        {{ c.text }}
      </div>
    </div>
  </div>
</template>
