<script lang="ts">
</script>

<script setup lang="ts">
// Composer footer (Composer.dc.html): hint · model · effort · mode · usage ring, with the model and mode pickers.
import type { SessionState } from '~/lib/session/types'

export type Hint = { keys?: string, text: string }[]

const props = defineProps<{ session: SessionState, hint?: Hint, initialMenu?: 'mode' | 'model' | '' }>()
const store = useSessionStore()

const modeOpen = ref(props.initialMenu === 'mode')
const modelOpen = ref(props.initialMenu === 'model')
const effortOpen = ref(false)
const contextOpen = ref(false)

const id = computed(() => props.session.meta.id)
const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
const efforts = computed(() => modelEfforts(model.value))
const effort = computed(() => fitEffort(store.effort[id.value] ?? 'medium', efforts.value))
const contextWindow = computed(() => model.value?.contextWindow || 200_000)
const pct = computed(() => Math.min(100, Math.round((props.session.contextTokens / contextWindow.value) * 100)))
const BTN = 'flex shrink-0 items-center gap-[5px] border px-1.5 py-0.5 text-[11.5px] whitespace-nowrap text-muted hover:text-toned'
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

    <UPopover
      v-model:open="modelOpen"
      :content="{ side: 'top', align: 'end', sideOffset: 6 }"
    >
      <button
        type="button"
        aria-label="Model"
        :class="[BTN, modelOpen ? 'border-accent' : 'border-transparent']"
      >
        <span class="text-strong">{{ session.meta.model }}</span><span class="max-sm:hidden">· {{ (model?.provider || 'openrouter').toLowerCase().replace(' · local', '') }}</span>
        <HIcon
          name="down"
          :size="11"
        />
      </button>
      <template #content>
        <div
          role="listbox"
          aria-label="Model"
          class="w-[380px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
        >
          <ModelList
            :selected="session.meta.model"
            @pick="store.setModel(id, $event); modelOpen = false"
          />
        </div>
      </template>
    </UPopover>

    <UPopover
      v-if="effort"
      v-model:open="effortOpen"
      :content="{ side: 'top', align: 'end', sideOffset: 6 }"
    >
      <button
        type="button"
        aria-label="Effort"
        :class="[BTN, effortOpen ? 'border-accent' : 'border-transparent', 'max-sm:hidden']"
      >
        effort <span :class="effort === efforts.at(-1) && efforts.length > 1 ? 'text-accent' : 'text-strong'">{{ effort }}</span><HIcon
          name="down"
          :size="11"
        />
      </button>
      <template #content>
        <div class="border border-accented bg-hover">
          <EffortSlider
            :model-value="effort"
            :levels="efforts"
            @update:model-value="store.setEffort(id, $event)"
          />
        </div>
      </template>
    </UPopover>

    <UPopover
      v-model:open="modeOpen"
      :content="{ side: 'top', align: 'end', sideOffset: 6 }"
    >
      <button
        type="button"
        aria-label="Permission mode"
        :class="[BTN, modeOpen ? 'border-accent' : 'border-transparent']"
      >
        mode <span class="text-strong">{{ MODE_LABEL[session.meta.permissionMode] }}</span><HIcon
          name="down"
          :size="11"
        />
      </button>
      <template #content>
        <div
          role="listbox"
          aria-label="Permission mode"
          class="w-[340px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
        >
          <div
            v-for="m in store.availableModes"
            :key="m.key"
            role="option"
            :aria-selected="m.key === session.meta.permissionMode"
            class="flex cursor-pointer flex-col rounded-none border-l-2 px-2.5 py-1.5"
            :class="[m.key === session.meta.permissionMode ? 'border-l-accent bg-raised' : 'border-l-transparent hover:bg-raised', m.sep ? 'border-t border-t-default' : '']"
            @click="store.setPermissionMode(id, m.key); modeOpen = false"
          >
            <span :class="m.cls">{{ m.label }}</span><span class="text-[11px] text-muted">{{ m.detail }}</span>
          </div>
        </div>
      </template>
    </UPopover>

    <UPopover
      v-model:open="contextOpen"
      :content="{ side: 'top', align: 'end', sideOffset: 6 }"
    >
      <button
        type="button"
        :aria-label="`Context: ${pct}% used`"
        :class="[BTN, contextOpen ? 'border-accent' : 'border-transparent']"
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
        <span class="text-strong">{{ pct }}%</span>
      </button>
      <template #content>
        <div
          role="dialog"
          aria-label="Context"
          class="w-[340px] max-w-[calc(100vw-32px)] border border-accented bg-hover px-[13px] py-3 text-[12.5px]"
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
