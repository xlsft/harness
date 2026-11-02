<script setup lang="ts">
// Background task (Task.dc.html): running with its output tail, finished notice, failed with the last line.
// A running task with no output yet is the one-line "Running … 0m 48s" status of Main.dc.html.
import type { TaskVM } from '~/lib/session/types'

const props = defineProps<{ task: TaskVM, sessionId: string }>()
const store = useSessionStore()
const { openPane } = usePanes()
const now = useNow()

const elapsed = computed(() => now.value - props.task.startedAt)
const tail = computed(() => props.task.tail.split('\n').filter(Boolean))
const lastLine = computed(() => tail.value[tail.value.length - 1] ?? '')
</script>

<template>
  <!-- running, no output yet -->
  <div
    v-if="task.state === 'running' && !task.tail"
    class="flex items-center gap-[9px] text-[12.5px] text-muted"
  >
    <HIcon
      name="loading"
      spin
      class="text-accent"
    />Running {{ task.description || task.command }} {{ formatClock(elapsed) }}
  </div>

  <!-- running with output -->
  <div
    v-else-if="task.state === 'running'"
    class="border border-default"
  >
    <div class="flex flex-wrap items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        name="background"
        class="text-muted"
      />
      <span class="text-muted">Background</span><span class="text-white">{{ task.command }}</span>
      <span class="flex-1" /><span class="text-[11px] text-accent">running · {{ formatDuration(elapsed) }}</span>
    </div>
    <pre class="m-0 overflow-x-auto bg-surface px-3 py-2 text-[12px] leading-[1.6] text-muted"><template
v-for="(l, i) in tail"
                                                                                                         :key="i"
><template
                                                                                                           v-for="(part, j) in l.split(/(https?:\/\/\S+)/)"
                                                                                                           :key="j"
                                                                                                         ><span
v-if="j % 2"
                                                                                                                class="text-accent"
    >{{ part }}</span><template v-else>{{ part }}</template></template>{{ i < tail.length - 1 ? '\n' : '' }}</template></pre>
    <div class="flex items-center gap-2 border-t border-default px-3 py-1.5 text-[11px] text-muted">
      {{ task.outputPath ? basename(task.outputPath) : '' }}
      <span class="flex-1" />
      <HBtn @click="openPane('terminal')">
        Open in terminal
      </HBtn>
      <HBtn
        tone="danger"
        @click="store.stopTask(sessionId, task.id)"
      >
        Stop
      </HBtn>
    </div>
  </div>

  <!-- finished -->
  <div
    v-else-if="task.state === 'done' || task.state === 'stopped'"
    class="flex flex-wrap items-center gap-[9px] border border-default px-3 py-2 text-[12.5px]"
  >
    <HIcon
      name="check"
      :class="task.state === 'done' ? 'text-ok' : 'text-muted'"
    />
    <span class="text-muted">{{ task.state === 'done' ? 'Background finished' : 'Background stopped' }}</span><span class="text-white">{{ task.command }}</span>
    <span class="flex-1" />
    <span
      class="text-[11px]"
      :class="task.state === 'done' ? 'text-ok' : 'text-muted'"
    >exit {{ task.exitCode }} · {{ formatDuration(task.durationMs) }}</span>
  </div>

  <!-- failed -->
  <div
    v-else
    class="border border-l-2 border-default border-l-bad"
  >
    <div class="flex flex-wrap items-center gap-[9px] px-3 py-2 text-[12.5px]">
      <HIcon
        name="warn"
        class="text-bad"
      />
      <span class="text-muted">Background failed</span><span class="text-white">{{ task.command }}</span>
      <span class="flex-1" /><span class="text-[11px] text-bad">exit {{ task.exitCode }} · {{ formatSeconds(task.durationMs) }}</span>
    </div>
    <pre
      v-if="lastLine"
      class="m-0 border-t border-default bg-surface px-3 py-[7px] text-[12px] leading-[1.6] text-toned"
    >{{ lastLine }}</pre>
    <div class="flex gap-2 border-t border-default px-3 py-1.5">
      <HBtn @click="openPane('tasks')">
        Full output
      </HBtn>
      <HBtn>Restart</HBtn>
    </div>
  </div>
</template>
