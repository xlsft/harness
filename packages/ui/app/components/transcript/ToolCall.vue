<script setup lang="ts">
// Tool call card (Tool.dc.html): bash and TaskOutput (output, running, error), read / glob / Wait rows, grep matches, web results,
// MCP input / output, edits as diffs (DiffView) and subagents (AgentCard). Output bodies scroll past 320px.
import type { ToolItem } from '~/lib/session/types'

// `headless`: just the body, under a row drawn by the parent (an expanded item of a ToolGroup).
const props = withDefaults(defineProps<{ tool: ToolItem, sessionId: string, headless?: boolean }>(), { headless: false })
const store = useSessionStore()
const { show } = useShow()
const now = useNow()
const t = computed(() => props.tool)
// Bash: open while the command runs, folded once it is done, unless the user toggled it.
const toggled = ref<boolean | null>(null)
const open = computed({
  // Headless, the parent's row already did the opening.
  get: () => toggled.value ?? (props.headless || t.value.name !== 'Bash' || t.value.status === 'running'),
  set: (v: boolean) => (toggled.value = v)
})
const meta = computed(() => toolMeta(t.value, now.value))
const mcp = computed(() => mcpParts(t.value.name))
const kind = computed(() => {
  if (t.value.name === 'Task' || t.value.name === 'Agent') return 'agent'
  if (isEditTool(t.value.name) && t.value.status === 'ok') return 'diff'
  if (t.value.name === 'Grep') return 'grep'
  if (t.value.name === 'WebSearch' || t.value.name === 'WebFetch') return 'web'
  if (mcp.value) return 'mcp'
  if (t.value.name === 'Show') return 'show'
  if (['Read', 'Glob', 'LS', 'Wait', 'Monitor'].includes(t.value.name)) return 'row'
  return 'bash'
})

// Bash's row says what the command does: "Run …" while it runs, "Ran …", "Failed to run …" (the title model's
// label, tool.title); until the label arrives, the model's own description, else the command.
const isBash = computed(() => t.value.name === 'Bash')
const command = computed(() => String(t.value.input.command ?? ''))
const commandLines = computed(() => highlightCode(command.value, 'bash'))
const label = computed(() => bashLabel(t.value))

// Bash moved to (or started in) the background: the note the agent got, shown under the command.
const remote = computed(() => !!store.sessions[props.sessionId]?.meta.remote)
const backgroundNote = computed(() => {
  const b = t.value.background
  if (!b) return ''
  const sent = String(t.value.output ?? '').trim()
  if (sent.includes(b.taskId)) return sent
  const why = { user: 'Moved to the background by you', queued_message: 'Moved to the background so your message could be delivered', agent: 'Started in the background by the agent', auto: 'Moved to the background after its timeout' }[b.reason]
  return `${why}. Background task ${b.taskId}.`
})
const cwdNote = computed(() => bashCwdNote(t.value))
// The command's own output (without the note when the result is just the note).
const bashOutput = computed(() => {
  const s = withoutCwdNote(outputLines.value.map(l => l.map(tk => tk.t).join('')).join('\n'), cwdNote.value)
  return backgroundNote.value && s.trim() === backgroundNote.value ? '' : s
})
const toast = useToast()
async function toBackground() {
  try {
    await store.transport?.backgroundToolCall(props.sessionId, t.value.id, 'user')
  } catch (e) {
    toast.add({ title: 'Could not move it to the background', description: e instanceof Error ? e.message : String(e), color: 'error' })
  }
}

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

// Grep output without file:line:text matches: its lines as they are, minus the "no matches" message.
const grepPlain = computed(() => String(t.value.result?.content ?? t.value.output ?? '').split('\n').map(l => l.trim())
  .filter(l => l && !/^no (matches|files) found/i.test(l)))

function marks(text: string) {
  const p = String(t.value.input.pattern ?? '')
  if (!p) return [{ t: text, m: false }]
  return text.split(new RegExp(`(${p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`)).map((s, i) => ({ t: s, m: i % 2 === 1 }))
}

const webResults = computed(() => {
  const r = t.value.result?.results
  return Array.isArray(r) ? r as { title: string, url: string }[] : []
})

// Show: what the call opened, to open it again.
const showTarget = computed(() => kind.value === 'show' && t.value.status === 'ok'
  ? showTargetOf(t.value.input, t.value.result, store.sessions[props.sessionId]?.meta.cwd ?? '')
  : null)
const showIn = computed(() => showTarget.value ? showPaneTitle(showTarget.value.kind) : '')

// Text of a failed call: the tool_result content (results of other kinds are JSON).
// (Tool errors come wrapped in a <tool_use_error> tag for the model; the card shows the message alone.)
const errorText = computed(() => String(t.value.output || t.value.result?.error || t.value.result?.message || '').replace(/<\/?tool_use_error>/g, '').trim())

const webText = computed(() => String(t.value.result?.result ?? t.value.output ?? '').trim())

// Headless under a labelled row: what the call really did (Bash shows its command, diffs their path).
const concrete = computed(() => props.headless && !isBash.value && kind.value !== 'diff' && String(t.value.input.description ?? '').trim() ? toolTarget(t.value) : '')

const hasBody = computed(() => !!concrete.value || (open.value && (
  t.value.status === 'error' && !isBash.value
    ? !!errorText.value
    : kind.value === 'bash'
      ? isBash.value || outputLines.value.some(l => l.length)
      : kind.value === 'web'
        ? webResults.value.length > 0 || !!webText.value
        // Show has a body only under a group's row, which cannot hold the Open button.
        : kind.value === 'show' ? props.headless && !!showTarget.value : kind.value !== 'row'
)))

// Favicons load straight from each site (no third-party icon service); a host without one shows its letter.
const noIcon = reactive<Record<string, boolean>>({})

const host = (url: string) => url.replace(/^https?:\/\//, '').split('/')[0]
</script>

<template>
  <AgentCard
    v-if="kind === 'agent'"
    :tool="tool"
  />

  <DiffView
    v-else-if="kind === 'diff'"
    :path="tool.name === 'NotebookEdit' ? `${toolTarget(tool)} · ${notebookCellLabel(tool)}` : toolTarget(tool)"
    :hunks="patchOf(tool)"
    :verb="tool.name === 'Write' ? 'Write' : 'Edit'"
    capped
  />

  <!-- Headless with nothing to show renders nothing, so the group drops the empty body and its border. -->
  <div
    v-else-if="!headless || hasBody"
    :class="headless ? '' : isBash ? ['overflow-hidden border', tool.status === 'error' ? 'border-bad' : 'border-default'] : ['overflow-hidden border border-default', tool.status === 'error' ? 'border-l-2 border-l-bad' : '']"
  >
    <!-- Bash: what it does, folded to that row once done -->
    <button
      v-if="!headless && isBash"
      type="button"
      class="flex w-full items-center gap-[9px] px-3 py-[7px] text-left text-[12.5px] text-muted"
      :class="hasBody ? 'border-b border-default' : ''"
      :aria-expanded="open"
      @click="open = !open"
    >
      <HIcon
        :name="tool.status === 'running' ? 'loading' : 'term'"
        :spin="tool.status === 'running'"
        :class="tool.status === 'running' ? 'text-accent' : tool.status === 'error' ? 'text-bad' : ''"
      />
      <span class="shrink-0">Bash</span>
      <span class="min-w-0 truncate">
        <span
          v-if="label.verb"
          :class="tool.status === 'error' ? 'mr-1 text-bad' : 'mr-1'"
        >{{ label.verb }}</span><span class="text-strong">{{ label.text }}</span>
      </span>
      <HIcon
        name="right"
        :size="11"
        class="shrink-0 transition-transform"
        :class="open && 'rotate-90'"
      />
      <span class="flex-1" />
      <span
        class="shrink-0 text-[11px]"
        :class="meta.cls === 'diff' ? '' : meta.cls"
      >{{ meta.text }}</span>
      <template v-if="tool.status === 'running'">
        <HBtn
          v-if="!remote"
          title="Keep it running as a background task and let the agent go on"
          @click.stop="toBackground"
        >
          Run in background
        </HBtn>
        <HBtn @click.stop="store.interrupt(sessionId)">
          Stop
        </HBtn>
      </template>
    </button>
    <div
      v-else-if="!headless"
      class="flex items-center gap-[9px] px-3 py-[7px] text-[12.5px] text-muted"
      :class="hasBody ? 'border-b border-default' : ''"
    >
      <HIcon
        :name="toolIcon(tool)"
        :spin="tool.status === 'running'"
        :class="tool.status === 'running' ? 'text-accent' : tool.status === 'error' ? 'text-bad' : ''"
      />
      <template v-if="mcp">
        <span class="border border-default px-[5px] text-[11px]">{{ mcp.server }}</span><span class="text-strong">{{ mcp.tool }}</span>
      </template>
      <template v-else>
        {{ tool.name }}<span class="min-w-0 truncate text-strong">{{ toolTarget(tool) }}</span>
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
      <HBtn
        v-else-if="showTarget"
        :title="`Open in the ${showIn} pane`"
        @click="show(showTarget)"
      >
        Open
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

    <!-- Under a row that shows the model's label: the call itself -->
    <p
      v-if="concrete"
      class="m-0 truncate bg-surface px-3 pt-[7px] pb-[3px] font-mono text-[12px] text-muted last:pb-[7px]"
      :title="concrete"
    >
      {{ concrete }}
    </p>

    <!-- Show under a group's row: the Open button -->
    <div
      v-if="kind === 'show' && headless && showTarget"
      class="flex items-center gap-2 px-3 py-[7px] text-[12px] text-muted"
    >
      <span class="min-w-0 flex-1 truncate">{{ showIn }} pane</span>
      <HBtn @click="show(showTarget)">
        Open
      </HBtn>
    </div>

    <!-- error of a tool other than Bash (Bash shows its errors in its own output): the message, readable -->
    <p
      v-else-if="tool.status === 'error' && !isBash && open && errorText"
      class="m-0 flex max-h-80 items-start gap-2 overflow-auto bg-surface px-3 py-2 text-[12.5px] leading-[1.6] text-bad"
    >
      <HIcon
        name="alert"
        :size="14"
        class="mt-[3px] shrink-0"
      />
      <span class="min-w-0 whitespace-pre-wrap">{{ errorText.charAt(0).toUpperCase() + errorText.slice(1) }}</span>
    </p>

    <!-- Bash: the whole command, then its output, dimmed -->
    <pre
      v-else-if="isBash && open"
      class="m-0 max-h-80 overflow-auto bg-surface px-3 py-[9px] text-[12px] leading-[1.6] whitespace-pre-wrap"
    ><span class="text-muted select-none">$ </span><template
v-for="(line, i) in commandLines"
:key="'c' + i"
><span
v-for="(tk, j) in line"
:key="j"
:style="{ color: tk.c }"
>{{ tk.t }}</span><template v-if="i < commandLines.length - 1">{{ '\n' }}</template></template><template v-if="bashOutput.trim()">{{ '\n' }}<span :class="tool.status === 'error' ? 'text-bad/80' : 'text-muted'">{{ bashOutput }}</span></template><template v-if="backgroundNote">{{ '\n' }}<span class="text-dimmed italic">{{ backgroundNote }}</span></template><template v-if="cwdNote">{{ '\n' }}<span class="text-dimmed">{{ cwdNote }}</span></template><span
      v-if="tool.status === 'running'"
      class="h-caret ml-0.5 inline-block h-[13px] w-[7px] bg-graphite-200 align-[-2px]"
    /></pre>

    <!-- bash output -->
    <pre
      v-else-if="kind === 'bash' && open && outputLines.some(l => l.length)"
      class="m-0 max-h-80 overflow-auto bg-surface px-3 py-[9px] text-[12px] leading-[1.6]"
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
      class="max-h-80 overflow-y-auto bg-surface py-1.5 text-[12px] leading-[1.65]"
    >
      <template
        v-for="(f, i) in grep"
        :key="f.file"
      >
        <div
          class="px-3 text-strong"
          :class="i > 0 ? 'pt-1' : ''"
        >
          {{ f.file }}
        </div>
        <div
          v-for="l in f.lines"
          :key="l.n"
          class="flex gap-3 overflow-hidden px-3 font-mono whitespace-pre"
        >
          <span class="w-[22px] text-right text-dimmed">{{ l.n }}</span>
          <span><template
            v-for="(p, k) in marks(l.text)"
            :key="k"
          ><span
            v-if="p.m"
            class="bg-accent text-on-accent"
          >{{ p.t }}</span><template v-else>{{ p.t }}</template></template></span>
        </div>
      </template>
      <!-- No match lines: file names only (files_with_matches, count), or nothing found -->
      <template v-if="!grep.length">
        <div
          v-for="line in grepPlain"
          :key="line"
          class="truncate px-3 font-mono text-toned"
        >
          {{ line }}
        </div>
        <p
          v-if="!grepPlain.length && tool.status !== 'running'"
          class="m-0 px-3 text-muted"
        >
          Nothing found
        </p>
      </template>
    </div>

    <!-- web results -->
    <div
      v-else-if="kind === 'web' && open"
      class="flex max-h-80 flex-col gap-px overflow-y-auto bg-surface px-1.5 py-1 text-[12px]"
    >
      <!-- WebFetch: the answer from the page (no result list) -->
      <p
        v-if="!webResults.length && webText"
        class="m-0 line-clamp-6 px-1.5 py-[3px] font-mono whitespace-pre-wrap text-toned"
      >
        {{ webText }}
      </p>
      <a
        v-for="r in webResults"
        :key="r.url"
        :href="r.url"
        target="_blank"
        rel="noopener"
        class="group flex min-w-0 items-center gap-2 px-1.5 py-[3px] text-toned hover:bg-raised"
      >
        <img
          v-if="!noIcon[host(r.url) ?? '']"
          :src="`https://${host(r.url)}/favicon.ico`"
          alt=""
          width="16"
          height="16"
          loading="lazy"
          referrerpolicy="no-referrer"
          class="size-4 shrink-0 bg-raised object-contain"
          @error="noIcon[host(r.url) ?? ''] = true"
        >
        <span
          v-else
          class="flex size-4 shrink-0 items-center justify-center bg-raised text-[10px] text-strong"
        >{{ (host(r.url) ?? '?')[0]!.toUpperCase() }}</span>
        <span class="min-w-0 truncate text-strong group-hover:underline">{{ r.title }}</span><span class="shrink-0 text-muted group-hover:text-toned">{{ host(r.url) }}</span>
      </a>
    </div>

    <!-- MCP input / output -->
    <div
      v-else-if="kind === 'mcp' && open"
      class="grid max-h-80 grid-cols-2 overflow-y-auto bg-surface text-[11.5px] leading-[1.6]"
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
