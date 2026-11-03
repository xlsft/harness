<script setup lang="ts">
// Square icon button: 28px in headers and the sidebar (`.hb` in the artboards), 30px in pane headers.
import type { IconName } from '~/utils/icons'

const props = withDefaults(defineProps<{
  icon: IconName
  label: string
  size?: 26 | 28 | 30 | 32
  iconSize?: number
  active?: boolean
  pressed?: boolean
  tooltip?: string
  kbd?: string
}>(), {
  size: 28,
  iconSize: 15,
  active: false,
  pressed: false,
  tooltip: '',
  kbd: ''
})

const box = computed(() => ({ 26: 'size-[26px]', 28: 'size-7', 30: 'size-[30px]', 32: 'size-8' }[props.size]))
</script>

<template>
  <UTooltip
    :text="tooltip || label"
    :kbds="kbd ? kbd.split('+') : undefined"
    :delay-duration="500"
    :content="{ side: 'bottom', sideOffset: 4 }"
  >
    <button
      type="button"
      :aria-label="label"
      :aria-pressed="pressed || undefined"
      class="flex shrink-0 items-center justify-center border hover:text-highlighted"
      :class="[box, active ? 'border-line text-strong' : pressed ? 'border-transparent bg-raised text-strong' : 'border-transparent text-muted']"
    >
      <HIcon
        :name="icon"
        :size="iconSize"
      />
    </button>
  </UTooltip>
</template>
