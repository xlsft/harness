<script setup lang="ts">
// The editor's tool glyphs; the design's icon set has no shapes.
import type { Tool } from '~/lib/session/annotate'

defineProps<{ tool: Exclude<Tool, 'pen'> }>()
const PATHS: Record<Exclude<Tool, 'pen'>, string> = {
  line: 'M5 19 19 5',
  arrow: 'M5 19 19 5M10 5h9v9',
  rect: 'M4 6h16v12H4z',
  ellipse: 'M3 12a9 7 0 1 0 18 0a9 7 0 1 0-18 0',
  step: 'M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0M10.5 9.5 12.5 8v8',
  redact: 'M4 6h16v12H4z',
  text: 'M5 7V5h14v2M12 5v14M9 19h6'
}
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    width="15"
    height="15"
    :fill="tool === 'redact' ? 'currentColor' : 'none'"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  ><path :d="PATHS[tool]" /></svg>
</template>
