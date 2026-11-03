<script setup lang="ts">
// Several tool calls in a row, with what the model said between them, folded into one line: the current action and
// the time while it runs, what was done once it is over. Open, a list: a row per call (expands into its card) and a
// line per remark (expands in place). Ctrl+O (verbose) opens them all.
import type { ToolItem } from '~/lib/session/types'

export type ClusterEntry
  = | { kind: 'tool', key: string, tool: ToolItem }
    | { kind: 'note', key: string, text: string, thinking: boolean }

const props = defineProps<{ entries: ClusterEntry[], sessionId: string }>()
const now = useNow()
const verbose = useState('transcript-verbose', () => false)
const opened = ref(false)
const open = computed(() => opened.value || verbose.value)
const notes = ref<Record<string, boolean>>({})

const tools = computed(() => props.entries.flatMap(e => e.kind === 'tool' ? [e.tool] : []))
const current = computed(() => tools.value.findLast(t => t.status === 'running'))
const failed = computed(() => tools.value.filter(t => t.status === 'error').length)

function action(t: ToolItem) {
  if (t.name === 'Bash') {
    const l = bashLabel(t)
    return l.verb ? `${l.verb} ${l.text}` : l.text
  }
  return `${t.name} ${toolTarget(t)}`.trim()
}

const store = useSessionStore()
const toast = useToast()
const remote = computed(() => !!store.sessions[props.sessionId]?.meta.remote)
async function toBackground() {
  if (!current.value) return
  try {
    await store.transport?.backgroundToolCall(props.sessionId, current.value.id, 'user')
  } catch (e) {
    toast.add({ title: 'Could not move it to the background', description: e instanceof Error ? e.message : String(e), color: 'error' })
  }
}

const GROUPS: { names: string[], one: string, many: (n: number) => string }[] = [
  { names: ['Bash'], one: 'ran a command', many: n => `ran ${n} commands` },
  { names: ['Read'], one: 'read a file', many: n => `read ${n} files` },
  { names: ['Edit', 'MultiEdit', 'Write'], one: 'edited a file', many: n => `edited ${n} files` },
  { names: ['Grep', 'Glob', 'LS'], one: 'searched once', many: n => `searched ${n} times` },
  { names: ['WebFetch', 'WebSearch'], one: 'looked up the web', many: n => `looked up the web ${n} times` }
]
const summary = computed(() => {
  const known = new Set(GROUPS.flatMap(g => g.names))
  const parts = GROUPS.flatMap((g) => {
    const n = tools.value.filter(t => g.names.includes(t.name)).length
    return n ? [n === 1 ? g.one : g.many(n)] : []
  })
  const other = tools.value.filter(t => !known.has(t.name)).length
  if (other) parts.push(other === 1 ? 'used a tool' : `used ${other} tools`)
  const s = parts.join(', ')
  return s.charAt(0).toUpperCase() + s.slice(1)
})
const time = computed(() => {
  if (current.value) return formatDuration(now.value - Math.min(...tools.value.map(t => t.startedAt || now.value)))
  return formatDuration(tools.value.reduce((n, t) => n + (t.durationMs || 0), 0))
})
</script>

<template>
  <div class="flex flex-col gap-2">
    <div class="flex min-w-0 items-center gap-3">
      <button
        type="button"
        class="flex min-w-0 items-center gap-[9px] rounded-none text-left text-[12.5px] text-muted hover:text-toned"
        :aria-expanded="open"
        @click="opened = !opened"
      >
        <HIcon
          :name="current ? 'loading' : failed ? 'alert' : 'check'"
          :spin="!!current"
          class="shrink-0"
          :class="current ? 'text-accent' : failed ? 'text-bad' : 'text-ok'"
        />
        <span class="min-w-0 truncate">{{ current ? action(current) : summary }}</span>
        <span
          v-if="!current && failed"
          class="shrink-0 text-bad"
        >{{ failed }} failed</span>
        <span class="shrink-0 text-[11px] tabular-nums">{{ time }}</span>
        <HIcon
          name="right"
          :size="11"
          class="shrink-0 transition-transform"
          :class="open && 'rotate-90'"
        />
      </button>
      <template v-if="current?.name === 'Bash' && !remote">
        <span class="flex-1" />
        <HBtn
          title="Keep it running as a background task and let the agent go on"
          @click="toBackground"
        >
          Run in background
        </HBtn>
      </template>
    </div>
    <div
      v-if="open"
      class="overflow-hidden border border-default"
    >
      <template
        v-for="(e, i) in entries"
        :key="e.key"
      >
        <ToolRow
          v-if="e.kind === 'tool'"
          :tool="e.tool"
          :session-id="sessionId"
          :first="i === 0"
        />
        <template v-else>
          <button
            type="button"
            class="flex w-full items-center gap-[9px] rounded-none px-3 py-[7px] text-left text-[12.5px] text-muted hover:bg-hover"
            :class="i > 0 && 'border-t border-default'"
            :aria-expanded="!!notes[e.key]"
            @click="notes[e.key] = !notes[e.key]"
          >
            <span
              class="min-w-0 flex-1 truncate"
              :class="e.thinking && 'italic'"
            >{{ e.text.replace(/\s+/g, ' ') }}</span>
            <HIcon
              name="right"
              :size="11"
              class="shrink-0 transition-transform"
              :class="notes[e.key] && 'rotate-90'"
            />
          </button>
          <div
            v-if="notes[e.key]"
            class="border-t border-default bg-surface px-3 py-2 text-[13px]"
          >
            <MarkdownBlock :text="e.text" />
          </div>
        </template>
      </template>
    </div>
  </div>
</template>
