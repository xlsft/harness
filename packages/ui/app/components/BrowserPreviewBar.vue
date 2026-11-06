<script setup lang="ts">
// The session's dev servers (the agent's Preview* tools) above the page: name, port, status, their logs and a stop
// control. The client owns the processes and nothing here is synced, so the bar asks it every few seconds while the
// pane is open.
import type { BrowserApi, PreviewServerVM } from '~/lib/browser/types'

const props = defineProps<{ sessionId: string, api: BrowserApi }>()

const POLL_MS = 3000
const servers = ref<PreviewServerVM[]>([])
const shown = computed(() => servers.value.filter(s => s.status !== 'stopped'))
const logsOf = ref('')
const errorsOnly = ref(false)
const lines = ref<string[]>([])
const failure = ref('')
const output = ref<HTMLElement | null>(null)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function refresh() {
  try {
    servers.value = await props.api.previewServers(props.sessionId)
  } catch {
    // Keeps the last list until the client answers again.
  }
  if (logsOf.value) await readLogs()
}

async function readLogs() {
  const id = logsOf.value
  try {
    const got = await props.api.previewLogs(props.sessionId, id, { errorsOnly: errorsOnly.value })
    if (logsOf.value !== id) return
    const el = output.value
    const atEnd = !el || el.scrollHeight - el.scrollTop - el.clientHeight < 8
    lines.value = got
    failure.value = ''
    if (atEnd) void nextTick(() => output.value?.scrollTo({ top: output.value.scrollHeight }))
  } catch (e) {
    failure.value = message(e)
  }
}

function toggleLogs(id: string) {
  logsOf.value = logsOf.value === id ? '' : id
  lines.value = []
  failure.value = ''
  if (logsOf.value) void readLogs()
}

async function stop(s: PreviewServerVM) {
  try {
    await props.api.stopPreview(props.sessionId, s.id)
  } catch (e) {
    failure.value = message(e)
  }
  await refresh()
}

let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  void refresh()
  timer = setInterval(() => void refresh(), POLL_MS)
})
onBeforeUnmount(() => clearInterval(timer))
watch(() => props.sessionId, () => {
  servers.value = []
  logsOf.value = ''
  lines.value = []
  void refresh()
})
watch(errorsOnly, () => {
  if (logsOf.value) void readLogs()
})

const dot = (s: PreviewServerVM) => (s.status === 'running' ? 'bg-ok' : s.status === 'starting' ? 'bg-warn' : 'bg-bad')
const statusText = (s: PreviewServerVM) => (s.status === 'exited' ? `exited ${s.exitCode}` : s.status === 'starting' ? 'starting' : '')
</script>

<template>
  <div
    v-if="shown.length"
    aria-label="Dev servers"
    class="shrink-0 border-b border-line bg-surface text-[12px] text-muted"
  >
    <div
      v-for="s in shown"
      :key="s.id"
      class="flex items-center gap-2 px-3 py-1"
      :title="s.commandLine"
    >
      <span
        aria-hidden="true"
        class="size-1.5 shrink-0"
        :class="dot(s)"
      />
      <span class="min-w-0 truncate text-strong">{{ s.name }}</span>
      <span class="shrink-0 tabular-nums">:{{ s.port }}</span>
      <span
        v-if="statusText(s)"
        class="shrink-0"
      >{{ statusText(s) }}</span>
      <span class="flex-1" />
      <HBtn
        :aria-pressed="logsOf === s.id"
        @click="toggleLogs(s.id)"
      >
        Logs
      </HBtn>
      <HBtn
        v-if="s.status === 'running' || s.status === 'starting'"
        tone="danger"
        @click="stop(s)"
      >
        Stop
      </HBtn>
    </div>
    <div
      v-if="logsOf"
      class="border-t border-line"
    >
      <label class="flex items-center gap-1.5 px-3 py-1">
        <input
          v-model="errorsOnly"
          type="checkbox"
        >
        Errors only
      </label>
      <p
        v-if="failure"
        role="alert"
        class="m-0 px-3 pb-1 text-bad"
      >
        {{ failure }}
      </p>
      <pre
        ref="output"
        class="m-0 max-h-[200px] overflow-auto px-3 pb-2 font-mono text-[11.5px] leading-[1.45] whitespace-pre-wrap text-strong"
      >{{ lines.length ? lines.join('\n') : 'No output.' }}</pre>
    </div>
  </div>
</template>
