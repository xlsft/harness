<script setup lang="ts">
// Parallel tool calls of one assistant message, as collapsed rows (the `.tool` box of Main.dc.html).
// A row expands into its full card; Ctrl+O (verbose) expands them all.
import type { ToolItem } from '~/lib/session/types'

defineProps<{ tools: ToolItem[], sessionId: string }>()
const verbose = useState('transcript-verbose', () => false)
</script>

<template>
  <div
    v-if="verbose"
    class="flex flex-col gap-[15px]"
  >
    <ToolCall
      v-for="t in tools"
      :key="t.id"
      :tool="t"
      :session-id="sessionId"
    />
  </div>
  <div
    v-else
    class="overflow-hidden border border-default"
  >
    <ToolRow
      v-for="(t, i) in tools"
      :key="t.id"
      :tool="t"
      :session-id="sessionId"
      :first="i === 0"
    />
  </div>
</template>
