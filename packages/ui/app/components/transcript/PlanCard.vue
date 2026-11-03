<script setup lang="ts">
// Plan approval (ExitPlanMode → PlanProposed). Not drawn in the artboards; built from the question and
// permission cards: plan in markdown, Approve and accept edits (1) / Approve, ask for edits (2) / Keep planning (esc).
import type { PlanItem } from '~/lib/session/types'

const props = defineProps<{ item: PlanItem, sessionId: string }>()
const store = useSessionStore()
const feedback = ref('')
const pending = computed(() => props.item.state === 'pending')

function answer(d: 'accept_edits' | 'manual' | 'keep_planning') {
  if (!pending.value) return
  store.respondToPlan(props.sessionId, props.item.id, d, d === 'keep_planning' ? feedback.value : '')
}

defineShortcuts(computed(() => pending.value
  ? { 1: () => answer('accept_edits'), 2: () => answer('manual') }
  : {}))

const label = computed(() => ({
  pending: '',
  approved_accept_edits: 'Plan approved · accepting edits',
  approved_manual: 'Plan approved · asking for each edit',
  keep_planning: 'Kept planning'
}[props.item.state]))
</script>

<template>
  <div
    class="border bg-surface"
    :class="pending ? 'border-cyan' : 'border-default'"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <span class="border border-cyan px-[5px] text-[11px] text-cyan">plan</span>
      <span class="text-strong">{{ pending ? 'Ready to code?' : label }}</span>
      <span class="flex-1" />
      <span class="truncate text-[11px] text-muted">{{ item.planFilePath }}</span>
    </div>
    <div class="px-3 py-2.5">
      <MarkdownBlock :text="item.plan" />
    </div>
    <div
      v-if="pending"
      class="flex flex-col gap-2 border-t border-default px-3 py-2"
    >
      <input
        v-model="feedback"
        placeholder="What should change? (keeps planning)"
        class="border border-default bg-default px-2.5 py-1.5 text-[12.5px] text-strong outline-none placeholder:text-muted focus:border-accent"
        @keydown.enter="answer('keep_planning')"
      >
      <div class="flex flex-wrap gap-2">
        <HBtn
          variant="primary"
          size="md"
          @click="answer('accept_edits')"
        >
          Approve, accept edits <span class="opacity-70">1</span>
        </HBtn>
        <HBtn
          size="md"
          @click="answer('manual')"
        >
          Approve, ask for edits <span class="text-muted">2</span>
        </HBtn>
        <HBtn
          size="md"
          @click="answer('keep_planning')"
        >
          Keep planning
        </HBtn>
      </div>
    </div>
  </div>
</template>
