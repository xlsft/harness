<script setup lang="ts">
// The Context section of the session details: one row (percentage and a bar) that expands into the breakdown
// (ContextBreakdown).
import type { SessionState } from '~/lib/session/types'
import { percentOf, type ContextUsageVM } from '~/lib/session/context'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()

const open = ref(false)
const usage = ref<ContextUsageVM | null>(null)

const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
// Collapsed, the row uses the last reported usage; the breakdown's own count once it is loaded.
const ctxWindow = computed(() => usage.value?.window || model.value?.contextWindow || 200_000)
const used = computed(() => usage.value?.used ?? props.session.contextTokens)
const pct = computed(() => Math.min(100, percentOf(used.value, ctxWindow.value)))
watch(() => props.session.meta.id, () => (usage.value = null))
</script>

<template>
  <section class="flex flex-col border-b border-default px-[13px] py-3">
    <button
      type="button"
      class="-mx-[9px] -my-1 flex items-center gap-2 rounded-none px-[9px] py-1 text-left hover:bg-hover"
      :aria-expanded="open"
      @click="open = !open"
    >
      <span class="text-[12px] text-muted">Context</span>
      <HIcon
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
      v-if="open"
      class="flex flex-col pt-2.5"
    >
      <ContextBreakdown
        v-model:usage="usage"
        :session="session"
      />
    </div>
  </section>
</template>
