<script setup lang="ts">
// Background task: one notice line, like the other transcript notices — running (with Stop), finished, stopped or
// failed (with its last output line), the command and how long it ran. Its output is in the Background tasks pane.
import type { TaskVM } from '~/lib/session/types'

const props = defineProps<{ task: TaskVM, sessionId: string }>()
const store = useSessionStore()
const { openPane } = usePanes()
const now = useNow()

const elapsed = computed(() => now.value - props.task.startedAt)
const lastLine = computed(() => props.task.tail.split('\n').filter(Boolean).at(-1) ?? '')
const label = computed(() => ({
  running: 'Background command running',
  done: 'Background command finished',
  stopped: 'Background command stopped'
} as Record<string, string>)[props.task.state] ?? 'Background command failed')
const failed = computed(() => !['running', 'done', 'stopped'].includes(props.task.state))
</script>

<template>
  <div class="flex flex-col gap-0.5 text-[12.5px] text-muted">
    <div class="flex min-w-0 items-center gap-[9px]">
      <HIcon
        :name="task.state === 'running' ? 'loading' : failed ? 'warn' : 'check'"
        :spin="task.state === 'running'"
        class="shrink-0"
        :class="task.state === 'running' ? 'text-accent' : failed ? 'text-bad' : task.state === 'done' ? 'text-ok' : ''"
      />
      <span
        class="shrink-0"
        :class="failed && 'text-bad'"
      >{{ label }}</span>
      <button
        type="button"
        class="min-w-0 truncate text-left font-mono text-[12px] text-toned hover:text-strong"
        :title="`${task.command} · open the Background tasks pane`"
        @click="openPane('tasks')"
      >
        {{ task.description || task.command }}
      </button>
      <span class="shrink-0 text-[11px] tabular-nums">
        <template v-if="task.state === 'running'">{{ formatClock(elapsed) }}</template>
        <template v-else>exit {{ task.exitCode }} · {{ formatDuration(task.durationMs) }}</template>
      </span>
      <button
        v-if="task.state === 'running'"
        type="button"
        class="shrink-0 text-[11.5px] hover:text-bad"
        @click="store.stopTask(sessionId, task.id)"
      >
        Stop
      </button>
    </div>
    <span
      v-if="failed && lastLine"
      class="truncate pl-6 font-mono text-[12px] text-dimmed"
    >{{ lastLine }}</span>
  </div>
</template>
