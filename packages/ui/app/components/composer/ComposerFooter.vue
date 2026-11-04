<script lang="ts">
</script>

<script setup lang="ts">
// Composer footer (Composer.dc.html): hint · model · effort · mode · usage ring, with the model and mode pickers.
import { sessionEffort, type ModelScope } from '~/lib/acp/modelChoices'
import type { EffortName, SessionState } from '~/lib/session/types'

export type Hint = { keys?: string, text: string }[]

const props = defineProps<{ session: SessionState, hint?: Hint }>()
const store = useSessionStore()

const contextOpen = ref(false)

const id = computed(() => props.session.meta.id)
// A session on an ACP agent shows the agent's own model, modes and context: its model ids say nothing about the
// server's catalog, even an equal one.
const acp = computed(() => props.session.meta.acp)
const onAgent = computed(() => props.session.meta.engine !== 'harness')
const model = computed(() => onAgent.value ? undefined : store.models.find(m => m.id === props.session.meta.model))
const scope = computed<ModelScope>(() => ({ kind: 'session', engine: props.session.meta.engine, acp: acp.value }))
const label = computed(() => acp.value?.models.find(m => m.id === acp.value!.currentModelId)?.name || acp.value?.currentModelId || model.value?.displayName || props.session.meta.model)
const selected = computed(() => onAgent.value ? acp.value?.currentModelId ?? '' : props.session.meta.model)
const acpModes = computed(() => onAgent.value ? { modes: acp.value?.modes ?? [], current: acp.value?.currentModeId ?? '' } : undefined)
const effortChoice = computed(() => sessionEffort({
  onAgent: onAgent.value,
  unsupported: props.session.meta.unsupported.includes('effort'),
  acp: acp.value,
  harness: modelEfforts(model.value),
  recommended: model.value?.defaultEffort,
  chosen: (props.session.meta.effort || store.effort[id.value] || 'medium') as EffortName
}))
function pickEffort(level: string) {
  if (onAgent.value) void store.setAcpEffort(id.value, level)
  else void store.setEffort(id.value, level as EffortName)
}
// An agent's window is what it reports, 0 until it does: no number of our own stands in for it.
const contextWindow = computed(() => onAgent.value ? Number(acp.value?.contextWindowTokens ?? 0) : model.value?.contextWindow || 200_000)
const contextUsed = computed(() => Number(acp.value?.contextUsedTokens ?? 0) || props.session.contextTokens)
const pct = computed(() => contextWindow.value ? Math.min(100, Math.round((contextUsed.value / contextWindow.value) * 100)) : 0)
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
      :model="selected"
      :model-engine="onAgent ? session.meta.engine : ''"
      :label="label"
      :provider="model?.provider"
      :scope="scope"
      :acp-modes="acpModes"
      :efforts="effortChoice.levels"
      :effort="effortChoice.current"
      :recommended="effortChoice.recommended"
      :mode="session.meta.permissionMode"
      @update:model="(m, engine) => store.setModel(id, m, engine)"
      @update:acp-mode="store.setAcpMode(id, $event)"
      @update:effort="pickEffort"
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
          <ContextBreakdown
            v-if="!session.meta.unsupported.includes('context_breakdown')"
            :session="session"
          />
          <p
            v-else
            class="m-0 text-muted"
          >
            {{ contextUsed.toLocaleString() }}{{ contextWindow ? ` of ${contextWindow.toLocaleString()}` : '' }} tokens
          </p>
          <LimitsBlock :session="session" />
        </div>
      </template>
    </UPopover>
  </div>
</template>
