<script setup lang="ts">
// Tool call card (Tool.dc.html): bash (output, running, error), read / glob rows, grep matches, web results,
// MCP input / output, edits as diffs (DiffView) and subagents (AgentCard).
import type { ToolItem } from '~/lib/session/types'

const props = defineProps<{ tool: ToolItem, sessionId: string }>()
const store = useSessionStore()
const now = useNow()
const open = ref(true)

const t = computed(() => props.tool)
const meta = computed(() => toolMeta(t.value, now.value))
const mcp = computed(() => mcpParts(t.value.name))
const kind = computed(() => {
  if (t.value.name === 'Task' || t.value.name === 'Agent') return 'agent'
  if (['Edit', 'MultiEdit', 'Write'].includes(t.value.name) && t.value.status === 'ok') return 'diff'
  if (t.value.name === 'Grep') return 'grep'
  if (t.value.name === 'WebSearch' || t.value.name === 'WebFetch') return 'web'
  if (mcp.value) return 'mcp'
  if (['Read', 'Glob', 'LS'].includes(t.value.name)) return 'row'
  return 'bash'
})

const output = computed(() => String(t.value.result?.stdout ?? t.value.output ?? ''))
const outputLines = computed(() => highlightOutput(t.value.status === 'error' ? [t.value.result?.stdout, t.value.result?.stderr, t.value.output].filter(Boolean).join('\n') || t.value.output : output.value))

const grep = computed(() => {
  const text = String(t.value.result?.content ?? t.value.output ?? '')
  const files: { file: string, lines: { n: string, text: string }[] }[] = []
  for (const line of text.split('\n')) {
    const m = line.match(/^(.+?):(\d+):(.*)$/)
    if (!m) continue
    let f = files[files.length - 1]
    if (!f || f.file !== m[1]) {
      f = { file: m[1]!, lines: [] }
      files.push(f)
    }
    f.lines.push({ n: m[2]!, text: m[3]! })
  }
  return files
})

function marks(text: string) {
  const p = String(t.value.input.pattern ?? '')
  if (!p) return [{ t: text, m: false }]
  return text.split(new RegExp(`(${p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`)).map((s, i) => ({ t: s, m: i % 2 === 1 }))
}

const webResults = computed(() => {
  const r = t.value.result?.results
  return Array.isArray(r) ? r as { title: string, url: string }[] : []
})

// Text of a failed call: the tool_result content (results of other kinds are JSON).
const errorText = computed(() => String(t.value.output || t.value.result?.error || t.value.result?.message || '').trim())

const webText = computed(() => String(t.value.result?.result ?? t.value.output ?? '').trim())

const hasBody = computed(() => open.value && (
  t.value.status === 'error' && kind.value !== 'bash'
    ? !!errorText.value
    : kind.value === 'bash' ? outputLines.value.some(l => l.length) : kind.value === 'web' ? webResults.value.length > 0 || !!webText.value : kind.value !== 'row'
))

const host = (url: string) => url.replace(/^https?:\/\//, '').split('/')[0]
</script>

<template>
  <AgentCard
    v-if="kind === 'agent'"
    :tool="tool"
  />

  <DiffView
    v-else-if="kind === 'diff'"
    :path="toolTarget(tool)"
    :hunks="patchOf(tool)"
    :verb="tool.name === 'Write' ? 'Write' : 'Edit'"
  />

  <div
    v-else
    class="border border-default"
    :class="tool.status === 'error' ? 'border-l-2 border-l-bad' : ''"
  >
    <div
      class="flex items-center gap-[9px] px-3 py-[7px] text-[12.5px] text-muted"
      :class="hasBody ? 'border-b border-default' : ''"
    >
      <HIcon
        :name="toolIcon(tool)"
        :spin="tool.status === 'running'"
        :class="tool.status === 'running' ? 'text-accent' : tool.status === 'error' ? 'text-bad' : ''"
      />
      <template v-if="mcp">
        <span class="border border-default px-[5px] text-[11px]">{{ mcp.server }}</span><span class="text-white">{{ mcp.tool }}</span>
      </template>
      <template v-else>
        {{ tool.name }}<span class="min-w-0 truncate text-white">{{ toolTarget(tool) }}</span>
        <template v-if="tool.name === 'Grep' && tool.input.path">
          in {{ tool.input.path }}
        </template>
      </template>
      <span class="flex-1" />
      <span
        class="shrink-0 text-[11px]"
        :class="meta.cls === 'diff' ? '' : meta.cls"
      >{{ meta.text }}</span>
      <HBtn
        v-if="tool.status === 'running'"
        @click="store.interrupt(sessionId)"
      >
        Stop
      </HBtn>
      <button
        v-else-if="kind === 'bash' && output"
        type="button"
        :aria-label="open ? 'Collapse output' : 'Expand output'"
        class="text-muted hover:text-highlighted"
        @click="open = !open"
      >
        <HIcon
          name="down"
          :size="14"
          :class="open ? '' : '-rotate-90'"
        />
      </button>
    </div>

    <!-- error of a non-shell tool (bash shows its errors in its own output) -->
    <pre
      v-if="tool.status === 'error' && kind !== 'bash' && open && errorText"
      class="m-0 overflow-x-auto bg-surface px-3 py-[9px] text-[12px] leading-[1.6] whitespace-pre-wrap text-bad"
    >{{ errorText }}</pre>

    <!-- bash output -->
    <pre
      v-else-if="kind === 'bash' && open && outputLines.some(l => l.length)"
      class="m-0 overflow-x-auto bg-surface px-3 py-[9px] text-[12px] leading-[1.6]"
      :class="tool.status === 'running' ? 'text-muted' : 'text-toned'"
    ><template
v-for="(line, i) in outputLines"
               :key="i"
><span
                 v-for="(tk, j) in line"
                 :key="j"
                 :style="{ color: tool.status === 'running' ? undefined : tk.c }"
               >{{ tk.t }}</span><template v-if="i < outputLines.length - 1">{{ '\n' }}</template></template><span
      v-if="tool.status === 'running'"
      class="h-caret ml-0.5 inline-block h-[13px] w-[7px] bg-graphite-200 align-[-2px]"
    /></pre>

    <!-- grep matches -->
    <div
      v-else-if="kind === 'grep' && open"
      class="bg-surface py-1.5 text-[12px] leading-[1.65]"
    >
      <template
        v-for="(f, i) in grep"
        :key="f.file"
      >
        <div
          class="px-3 text-white"
          :class="i > 0 ? 'pt-1' : ''"
        >
          {{ f.file }}
        </div>
        <div
          v-for="l in f.lines"
          :key="l.n"
          class="flex gap-3 overflow-hidden px-3 whitespace-pre"
        >
          <span class="w-[22px] text-right text-dimmed">{{ l.n }}</span>
          <span><template
            v-for="(p, k) in marks(l.text)"
            :key="k"
          ><span
            v-if="p.m"
            class="bg-accent text-white"
          >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
        </div>
      </template>
    </div>

    <!-- web results -->
    <div
      v-else-if="kind === 'web' && open"
      class="flex flex-col gap-1 bg-surface px-3 py-1.5 text-[12px]"
    >
      <!-- WebFetch: the answer from the page (no result list) -->
      <p
        v-if="!webResults.length && webText"
        class="m-0 line-clamp-6 whitespace-pre-wrap text-toned"
      >
        {{ webText }}
      </p>
      <a
        v-for="r in webResults"
        :key="r.url"
        :href="r.url"
        target="_blank"
        rel="noopener"
        class="flex gap-2 text-toned"
      >
        <span class="flex size-4 shrink-0 items-center justify-center bg-raised text-[10px] text-white">{{ (host(r.url) ?? '?')[0]!.toUpperCase() }}</span>
        <span class="text-white">{{ r.title }}</span><span class="text-muted">{{ host(r.url) }}</span>
      </a>
    </div>

    <!-- MCP input / output -->
    <div
      v-else-if="kind === 'mcp' && open"
      class="grid grid-cols-2 bg-surface text-[11.5px] leading-[1.6]"
    >
      <div class="min-w-0 border-r border-default px-3 py-2">
        <div class="pb-1 text-muted">
          Input
        </div>
        <pre class="m-0 whitespace-pre-wrap text-toned"><span
v-for="(tk, j) in highlightJson(tool.input)"
                                                              :key="j"
:style="{ color: tk.c }"
        >{{ tk.t }}</span></pre>
      </div>
      <div class="min-w-0 px-3 py-2">
        <div class="pb-1 text-muted">
          Output
        </div>
        <pre class="m-0 whitespace-pre-wrap text-toned"><span
v-for="(tk, j) in highlightJson(tool.result ?? tool.output)"
                                                              :key="j"
:style="{ color: tk.c }"
        >{{ tk.t }}</span></pre>
      </div>
    </div>
  </div>
</template>
