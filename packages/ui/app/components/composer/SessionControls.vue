<script setup lang="ts">
// The model, effort and permission mode chips with their menus: the same on the new-session screen and in a session.
// With an ACP agent's model (acpModes set) the mode chip follows what the agent reports, and goes away without modes;
// the effort levels are whatever the caller was told, the agent's own included.
import type { AcpMode } from '~/gen/harness/agent/v1/acp_pb'
import type { ModelScope } from '~/lib/acp/modelChoices'
import type { PermissionModeName } from '~/lib/session/types'
import type { EffortLevel } from '~/utils/composer'

const props = defineProps<{
  model: string
  // The engine of `model` when it is an agent's.
  modelEngine?: string
  label: string
  provider?: string
  efforts: EffortLevel[]
  effort?: string
  mode?: PermissionModeName
  scope?: ModelScope
  acpModes?: { modes: AcpMode[], current: string }
}>()
const emit = defineEmits<{
  'update:model': [id: string, engine: string]
  'update:effort': [level: string]
  'update:mode': [mode: PermissionModeName]
  'update:acpMode': [modeId: string]
}>()
const currentAcpMode = computed(() => props.acpModes?.modes.find(m => m.id === props.acpModes!.current))
const effortLabel = computed(() => props.efforts.find(l => l.id === props.effort)?.label ?? props.effort)
const store = useSessionStore()
const menu = ref<'' | 'model' | 'effort' | 'mode'>('')
watch(menu, (m) => {
  if (m === 'model') void store.refreshModels().catch(() => {})
})
const open = (m: 'model' | 'effort' | 'mode') => ({
  'open': menu.value === m,
  'onUpdate:open': (v: boolean) => (menu.value = v ? m : '')
})
const CHIP = 'flex shrink-0 items-center gap-1.5 px-2 py-1 text-[11.5px] whitespace-nowrap text-muted hover:bg-hover hover:text-toned'
const POPOVER = { side: 'top', align: 'end', sideOffset: 6 } as const
</script>

<template>
  <UPopover
    v-bind="open('model')"
    :content="POPOVER"
  >
    <button
      type="button"
      aria-label="Model"
      :class="[CHIP, menu === 'model' && 'bg-hover']"
    >
      <span
        class="text-strong"
        :title="provider"
      >{{ label }}</span>
    </button>
    <template #content>
      <div
        role="listbox"
        aria-label="Model"
        class="w-[380px] max-w-[calc(100vw-32px)] overflow-hidden border border-accented bg-hover text-[12.5px]"
      >
        <ModelList
          :selected="model"
          :selected-engine="modelEngine"
          :scope="scope"
          @pick="(id, engine) => { emit('update:model', id, engine); menu = '' }"
        />
      </div>
    </template>
  </UPopover>

  <UPopover
    v-if="effort && efforts.length"
    v-bind="open('effort')"
    :content="POPOVER"
  >
    <button
      type="button"
      aria-label="Effort"
      :class="[CHIP, menu === 'effort' && 'bg-hover', 'max-sm:hidden @max-[560px]:hidden']"
    >
      {{ effortLabel }}
    </button>
    <template #content>
      <div class="overflow-hidden border border-accented bg-hover">
        <EffortSlider
          :model-value="effort"
          :levels="efforts"
          @update:model-value="emit('update:effort', $event)"
        />
      </div>
    </template>
  </UPopover>

  <UPopover
    v-if="acpModes?.modes.length"
    v-bind="open('mode')"
    :content="POPOVER"
  >
    <button
      type="button"
      aria-label="Agent mode"
      :class="[CHIP, menu === 'mode' && 'bg-hover']"
    >
      <span class="text-strong">{{ currentAcpMode?.name ?? acpModes.current }}</span>
    </button>
    <template #content>
      <div
        role="listbox"
        aria-label="Agent mode"
        class="w-[340px] max-w-[calc(100vw-32px)] overflow-hidden border border-accented bg-hover text-[12.5px]"
      >
        <div
          v-for="m in acpModes.modes"
          :key="m.id"
          role="option"
          :aria-selected="m.id === acpModes.current"
          class="flex cursor-pointer flex-col rounded-none border-l-2 px-2.5 py-1.5"
          :class="m.id === acpModes.current ? 'border-l-accent bg-raised' : 'border-l-transparent hover:bg-raised'"
          @click="emit('update:acpMode', m.id); menu = ''"
        >
          <span class="text-strong">{{ m.name }}</span><span
            v-if="m.description"
            class="text-[11px] text-muted"
          >{{ m.description }}</span>
        </div>
      </div>
    </template>
  </UPopover>

  <UPopover
    v-else-if="!acpModes && mode"
    v-bind="open('mode')"
    :content="POPOVER"
  >
    <button
      type="button"
      aria-label="Permission mode"
      :class="[CHIP, menu === 'mode' && 'bg-hover']"
    >
      <span class="text-strong">{{ MODE_LABEL[mode] }}</span>
    </button>
    <template #content>
      <div
        role="listbox"
        aria-label="Permission mode"
        class="w-[340px] max-w-[calc(100vw-32px)] overflow-hidden border border-accented bg-hover text-[12.5px]"
      >
        <div
          v-for="m in store.availableModes"
          :key="m.key"
          role="option"
          :aria-selected="m.key === mode"
          class="flex cursor-pointer flex-col rounded-none border-l-2 px-2.5 py-1.5"
          :class="[m.key === mode ? 'border-l-accent bg-raised' : 'border-l-transparent hover:bg-raised', m.sep ? 'border-t border-t-default' : '']"
          @click="emit('update:mode', m.key); menu = ''"
        >
          <span :class="m.cls">{{ m.label }}</span><span class="text-[11px] text-muted">{{ m.detail }}</span>
        </div>
      </div>
    </template>
  </UPopover>
</template>
