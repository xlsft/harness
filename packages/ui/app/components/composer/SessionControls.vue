<script setup lang="ts">
// The model, effort and permission mode chips with their menus: the same on the new-session screen and in a session.
import type { EffortName, PermissionModeName } from '~/lib/session/types'

defineProps<{ model: string, label: string, provider?: string, efforts: EffortName[], effort?: EffortName, mode: PermissionModeName }>()
const emit = defineEmits<{ 'update:model': [id: string], 'update:effort': [level: EffortName], 'update:mode': [mode: PermissionModeName] }>()
const store = useSessionStore()
const menu = ref<'' | 'model' | 'effort' | 'mode'>('')
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
          @pick="emit('update:model', $event); menu = ''"
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
      effort <span :class="effort === efforts.at(-1) && efforts.length > 1 ? 'text-accent' : 'text-strong'">{{ effort }}</span>
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
