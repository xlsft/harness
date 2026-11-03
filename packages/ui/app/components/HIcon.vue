<script setup lang="ts">
// One icon set for the whole UI: the inline SVG paths of the design artboards (utils/icons.ts).
// 24×24 viewBox, fill = currentColor, 15 px by default.
import { ICONS, type IconName } from '~/utils/icons'

const props = withDefaults(defineProps<{
  name: IconName
  size?: number | string
  spin?: boolean
}>(), {
  size: 15,
  spin: false
})

const px = computed(() => typeof props.size === 'number' ? `${props.size}px` : props.size)
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    aria-hidden="true"
    class="shrink-0 fill-current"
    :style="{ width: px, height: px }"
  ><path
    :d="ICONS[name]"
    :class="{ 'h-spin': spin }"
  /></svg>
</template>
