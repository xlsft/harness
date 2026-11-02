<script setup lang="ts">
// 30px square button of the pane headers: bare #8f8f8f icon, #1c1c1c fill while its menu is open.
// `white` keeps the icon white at rest (the artboards draw the settings button that way).
import type { IconName } from '~/utils/icons'

withDefaults(defineProps<{
  icon: IconName
  label: string
  pressed?: boolean
  white?: boolean
  disabled?: boolean
  iconSize?: number
}>(), {
  pressed: false,
  white: false,
  disabled: false,
  iconSize: 15
})
</script>

<template>
  <button
    type="button"
    :aria-label="label"
    :title="label"
    class="flex size-[30px] shrink-0 items-center justify-center"
    :class="[
      pressed ? 'bg-raised' : 'hover:bg-hover',
      disabled ? 'text-disabled' : pressed || white ? 'text-white' : 'text-muted hover:text-white'
    ]"
  >
    <HIcon
      :name="icon"
      :size="iconSize"
    />
  </button>
</template>
