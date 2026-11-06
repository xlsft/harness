<script setup lang="ts">
// The Context section of the session details: one row (percentage and a bar) that expands into the breakdown
// (ContextBreakdown).
import type { SessionState } from '~/lib/session/types'
import { percentOf, type ContextUsageVM } from '~/lib/session/context'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()

const open = ref(false)
const usage = ref<ContextUsageVM | null>(null)

const model = computed(() => props.session.meta.engine === 'harness' ? store.models.find(m => m.id === props.session.meta.model) : undefined)
// Collapsed, the row uses the last reported usage; the breakdown's own count once it is loaded. An agent reports its
// totals only, so a session on one has no breakdown to expand into.
const acp = computed(() => props.session.meta.acp)
const breakdown = computed(() => !props.session.meta.unsupported.includes('context_breakdown'))
const ctxWindow = computed(() => usage.value?.window || Number(acp.value?.contextWindowTokens ?? 0) || model.value?.contextWindow || 200_000)
const used = computed(() => usage.value?.used ?? (Number(acp.value?.contextUsedTokens ?? 0) || props.session.contextTokens))
const pct = computed(() => Math.min(100, percentOf(used.value, ctxWindow.value)))
watch(() => props.session.meta.id, () => (usage.value = null))
</script>

<template>
  <section class="flex flex-col border-b border-default px-[13px] py-3">
    <button
      type="button"
      class="-mx-[9px] -my-1 flex items-center gap-2 rounded-none px-[9px] py-1 text-left hover:bg-hover"
      :aria-expanded="breakdown ? open : undefined"
      :disabled="!breakdown"
      @click="open = !open"
    >
      <span class="text-[12px] text-muted">Context</span>
      <HIcon
        v-if="breakdown"
        name="right"
        :size="12"
        class="shrink-0 text-muted transition-transform"
        :class="open && 'rotate-90'"
      />
      <span class="flex-1" />
      <span class="shrink-0 text-[12px] text-strong tabular-nums">{{ pct }}%</span>
      <span class="flex h-1 w-12 shrink-0 bg-raised">
        <span
          class="bg-accent"
          :style="{ width: `${pct}%` }"
        />
      </span>
    </button>

    <div
      v-if="open && breakdown"
      class="flex flex-col pt-2.5"
    >
      <ContextBreakdown
        v-model:usage="usage"
        :session="session"
      />
    </div>
  </section>
</template>
