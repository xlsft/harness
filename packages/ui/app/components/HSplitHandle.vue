<script setup lang="ts">
// Divider between split panes (Reka UI SplitterResizeHandle): a bright hairline so every split is visible,
// accent-coloured while hovered or dragged. The hit area is wider than the line.
import { SplitterResizeHandle } from 'reka-ui'

defineProps<{ dir: 'row' | 'col' }>()
</script>

<template>
  <SplitterResizeHandle
    :hit-area-margins="{ coarse: 12, fine: 4 }"
    class="relative z-10 shrink-0 bg-(--ui-divider) transition-colors hover:bg-primary data-[state=drag]:bg-primary"
    :class="dir === 'row' ? 'w-px' : 'h-px'"
  />
</template>
