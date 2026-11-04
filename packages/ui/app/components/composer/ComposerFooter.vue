<script lang="ts">
</script>

<script setup lang="ts">
// Composer footer (Composer.dc.html): hint · model · effort · mode · usage ring, with the model and mode pickers.
import type { EffortName, SessionState } from '~/lib/session/types'

export type Hint = { keys?: string, text: string }[]

const props = defineProps<{ session: SessionState, hint?: Hint }>()
const store = useSessionStore()

const contextOpen = ref(false)

const id = computed(() => props.session.meta.id)
const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
const efforts = computed(() => modelEfforts(model.value))
const effort = computed(() => fitEffort((props.session.meta.effort || store.effort[id.value] || 'medium') as EffortName, efforts.value))
const contextWindow = computed(() => model.value?.contextWindow || 200_000)
const pct = computed(() => Math.min(100, Math.round((props.session.contextTokens / contextWindow.value) * 100)))
const BTN = 'flex shrink-0 items-center gap-[5px] px-2 py-1 text-[11.5px] whitespace-nowrap text-muted hover:bg-hover hover:text-toned'
</script>

<template>
  <div class="flex min-w-0 flex-nowrap items-center gap-0.5 text-[11.5px] whitespace-nowrap text-muted">
    <span class="flex min-w-0 flex-1 items-center gap-2.5 overflow-hidden">
      <span
        v-for="(h, i) in hint"
        :key="i"
        class="inline-flex shrink-0 items-center gap-1"
      >
        <HKeys
          v-if="h.keys"
          :keys="h.keys"
        />{{ h.text }}
      </span>
    </span>

    <SessionControls
      :model="session.meta.model"
      :label="model?.displayName || session.meta.model"
      :provider="model?.provider"
      :efforts="efforts"
      :effort="effort"
      :mode="session.meta.permissionMode"
      @update:model="store.setModel(id, $event)"
      @update:effort="store.setEffort(id, $event)"
      @update:mode="store.setPermissionMode(id, $event)"
    />

    <UPopover
      v-model:open="contextOpen"
      :content="{ side: 'top', align: 'end', sideOffset: 6 }"
    >
      <button
        type="button"
        :aria-label="`Context: ${pct}% used`"
        :class="[BTN, contextOpen && 'bg-hover']"
      >
        <svg
          viewBox="0 0 20 20"
          class="size-[15px]"
          aria-hidden="true"
        >
          <circle
            cx="10"
            cy="10"
            r="8"
            fill="none"
            stroke="var(--color-line)"
            stroke-width="3"
          />
          <circle
            cx="10"
            cy="10"
            r="8"
            fill="none"
            stroke="var(--color-accent)"
            stroke-width="3"
            :stroke-dasharray="`${(pct / 100) * 50.27} 50.27`"
            transform="rotate(-90 10 10)"
          />
        </svg>
      </button>
      <template #content>
        <div
          role="dialog"
          aria-label="Context"
          class="w-[340px] max-w-[calc(100vw-32px)] overflow-hidden border border-accented bg-hover px-[13px] py-3 text-[12.5px]"
        >
          <div class="mb-2.5 text-[12px] text-muted">
            Context
          </div>
          <ContextBreakdown :session="session" />
          <LimitsBlock :session="session" />
        </div>
      </template>
    </UPopover>
  </div>
</template>
