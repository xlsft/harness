<script setup lang="ts">
// Parallel tool calls of one assistant message, as collapsed rows (the `.tool` box of Main.dc.html).
// A row expands into its full card; Ctrl+O (verbose) expands them all.
import type { ToolItem } from '~/lib/session/types'

defineProps<{ tools: ToolItem[], sessionId: string }>()
const now = useNow()
const verbose = useState('transcript-verbose', () => false)
const open = ref<Record<string, boolean>>({})
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
    class="border border-default"
  >
    <template
      v-for="(t, i) in tools"
      :key="t.id"
    >
      <button
        type="button"
        class="flex w-full items-center gap-[9px] px-3 py-[7px] text-left text-[12.5px] text-muted hover:bg-hover"
        :class="i > 0 ? 'border-t border-default' : ''"
        :aria-expanded="!!open[t.id]"
        @click="open[t.id] = !open[t.id]"
      >
        <HIcon
          :name="t.name === 'Task' || t.name === 'Agent' ? 'agent' : toolIcon(t)"
          :spin="t.status === 'running' && t.name !== 'Task' && t.name !== 'Agent'"
        />
        {{ t.name === 'Task' ? 'Agent' : t.name }} <span class="min-w-0 truncate text-white">{{ toolTarget(t) }}</span>
        <span class="flex-1" />
        <span
          v-if="toolMeta(t, now, true).cls === 'diff'"
          class="shrink-0 text-[10px]"
        >
          <span class="text-ok">{{ toolMeta(t, now, true).text.split(' ')[0] }}</span> <span class="text-bad">{{ toolMeta(t, now, true).text.split(' ')[1] }}</span>
        </span>
        <span
          v-else
          class="shrink-0"
          :class="[toolMeta(t, now, true).cls, toolMeta(t, now, true).cls ? 'text-[10.5px]' : 'text-[10px]']"
        >{{ toolMeta(t, now, true).text }}</span>
      </button>
      <div
        v-if="open[t.id]"
        class="border-t border-default empty:hidden"
        :class="['Edit', 'MultiEdit', 'Write', 'Task', 'Agent'].includes(t.name) && 'p-2'"
      >
        <ToolCall
          :tool="t"
          :session-id="sessionId"
          headless
        />
      </div>
    </template>
  </div>
</template>
