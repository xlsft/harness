<script setup lang="ts">
// Transcript (Main.dc.html): bottom-anchored column (column-reverse keeps it pinned to the newest item),
// 790px max, 15px between items. Parallel tool calls of one message collapse into a ToolGroup.
import type { SessionState, ToolItem, TranscriptItem, UserItem } from '~/lib/session/types'
import { hasActions, pendingMessages } from '~/lib/session/delivery'
import { chaptersOf, linkedSessionIds, SESSION_CARD_TOOLS } from '~/lib/session/links'
import type { ClusterEntry } from '~/components/transcript/ToolCluster.vue'
import { findingsOf, sentFilesOf } from '~/lib/session/userTools'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()

// Tool calls whose own card replaces them.
const HIDDEN_TOOLS = new Set(['AskUserQuestion', 'ExitPlanMode', 'TodoWrite', 'WrapUp'])

type Block
  = | { kind: 'item', key: string, item: TranscriptItem }
    | { kind: 'group', key: string, tools: ToolItem[] }
    | { kind: 'visual', key: string, versions: ToolItem[] }
    | { kind: 'cluster', key: string, entries: ClusterEntry[] }
    | { kind: 'files', key: string, tool: ToolItem }
    | { kind: 'findings', key: string, tool: ToolItem }

// Visual calls by id: one card per id, at its latest call, with the earlier calls as versions.
const isVisual = (i: TranscriptItem) => i.kind === 'tool' && i.name === 'Visual' && i.status !== 'error'
const visuals = computed(() => {
  const by = new Map<string, ToolItem[]>()
  for (const item of props.session.items) {
    if (item.kind !== 'tool' || !isVisual(item)) continue
    const id = String(item.input.id ?? item.id)
    by.set(id, [...(by.get(id) ?? []), item])
  }
  return by
})

// Tools that stay on their own: agents have their own card, entering plan mode is a notice.
const UNCLUSTERED = new Set(['Task', 'Agent', 'EnterPlanMode', ...SESSION_CARD_TOOLS])
const clusterable = (b: Block) => b.kind === 'group' || (b.kind === 'item' && ((b.item.kind === 'tool' && !UNCLUSTERED.has(b.item.name)) || b.item.kind === 'assistant'))
const isToolBlock = (b: Block) => b.kind === 'group' || (b.kind === 'item' && b.item.kind === 'tool')
function clusterEntries(run: Block[]): ClusterEntry[] {
  return run.flatMap((b): ClusterEntry[] => {
    if (b.kind === 'group') return b.tools.map(t => ({ kind: 'tool', key: t.id, tool: t }))
    if (b.kind !== 'item') return []
    if (b.item.kind === 'tool') return [{ kind: 'tool', key: b.item.id, tool: b.item }]
    if (b.item.kind !== 'assistant') return []
    const id = b.item.id
    return b.item.blocks.flatMap((blk, i): ClusterEntry[] => blk.text.trim() ? [{ kind: 'note', key: `${id}:${i}`, text: blk.text, thinking: blk.type === 'thinking' }] : [])
  })
}
// Runs of tool calls (with what the model said between them) fold into one cluster from the first call, so the
// layout does not jump when the next one comes; what the model says after the last call stays out, it is the answer.
function clustered(blocks: Block[]): Block[] {
  const out: Block[] = []
  let run: Block[] = []
  const flush = () => {
    let end = run.length
    while (end > 0 && !isToolBlock(run[end - 1]!)) end--
    let start = 0
    while (start < end && !isToolBlock(run[start]!)) start++
    const core = run.slice(start, end)
    const entries = clusterEntries(core)
    if (entries.some(e => e.kind === 'tool')) {
      out.push(...run.slice(0, start), { kind: 'cluster', key: 'c:' + core[0]!.key, entries }, ...run.slice(end))
    } else out.push(...run)
    run = []
  }
  for (const b of blocks) {
    if (clusterable(b)) run.push(b)
    else {
      flush()
      out.push(b)
    }
  }
  flush()
  return out
}

// ReportFindings: each result is the whole list, so one card, at the latest report.
const latestFindings = computed(() => props.session.items.findLast(i => i.kind === 'tool' && !!findingsOf(i)))

// A plan with work left stays under the latest messages; once every item is done it stays where it was last updated.
const pinnedPlan = computed(() => {
  const item = props.session.items.findLast(i => i.kind === 'todos')
  return item?.kind === 'todos' && item.todos.some(t => t.status !== 'completed') ? item : undefined
})

const allBlocks = computed<Block[]>(() => {
  const out: Block[] = []
  for (const item of props.session.items) {
    if (item === pinnedPlan.value) continue
    if (item.kind === 'tool' && sentFilesOf(item)) {
      out.push({ kind: 'files', key: 'f:' + item.id, tool: item })
      continue
    }
    if (item.kind === 'tool' && findingsOf(item)) {
      if (item === latestFindings.value) out.push({ kind: 'findings', key: 'r:' + item.id, tool: item })
      continue
    }
    if (item.kind === 'tool' && isVisual(item)) {
      const versions = visuals.value.get(String(item.input.id ?? item.id))!
      if (versions[versions.length - 1] === item) out.push({ kind: 'visual', key: 'v:' + String(item.input.id ?? item.id), versions })
      continue
    }
    if (item.kind === 'tool') {
      if (HIDDEN_TOOLS.has(item.name)) continue
      // The access card replaces the call; after a client restart there is no card, and the row stays.
      if (item.name === 'ComputerAccess' && props.session.items.some(i => i.kind === 'computerAccess' && i.toolUseId === item.id)) continue
      if (item.grouped && !SESSION_CARD_TOOLS.has(item.name)) {
        const last = out[out.length - 1]
        if (last?.kind === 'group' && last.tools[0]?.messageId === item.messageId) {
          last.tools.push(item)
          continue
        }
        out.push({ kind: 'group', key: 'g:' + item.id, tools: [item] })
        continue
      }
    }
    out.push({ kind: 'item', key: item.kind + ':' + item.id, item })
  }
  return clustered(out)
})

// Messages not read by the agent yet: handed to the client (sending, sent), in the client's queue, then in this
// browser's queue (lib/session/delivery.ts).
const queued = computed(() => pendingMessages(props.session, store.outbox[props.session.meta.id]))
const waiting = computed(() => queued.value.filter(q => q.delivery === 'queued' || q.delivery === 'held').length)
const working = computed(() => props.session.meta.status === 'working')
const now = useNow()
const activity = computed(() => {
  const s = props.session
  const todo = s.todos.find(t => t.status === 'in_progress')
  if (todo?.activeForm) return todo.activeForm
  // A running tool call says what it does in its own row just above.
  if (s.items.some(i => i.kind === 'tool' && i.status === 'running' && i.name !== 'Task')) return ''
  const last = s.items[s.items.length - 1]
  if (last?.kind === 'assistant' && last.streaming) return last.blocks[last.blocks.length - 1]?.type === 'thinking' ? 'Thinking' : 'Writing'
  return 'Thinking'
})
const busy = computed(() => working.value || props.session.meta.status === 'needs_input')
// Another machine's session not driven from here offers Copy only.
const readOnly = computed(() => props.session.meta.remote && !(store.remoteControl[props.session.meta.id] && props.session.meta.remoteControllable))
// The last user message keeps its actions row visible; the others show it on hover.
const lastUser = computed(() => {
  const q = queued.value[queued.value.length - 1]
  if (q) return 'p:' + q.id
  const u = props.session.items.findLast(i => i.kind === 'user' && hasActions(i))
  return u ? 'u:' + u.id : ''
})

const toast = useToast()
const route = useRoute()
// Message actions; a failure shows a toast.
async function act(title: string, fn: () => unknown) {
  try {
    await fn()
  } catch (err) {
    toast.add({ title, description: (err instanceof Error ? err.message : String(err)).replace(/^\[\w+\]\s*/, ''), color: 'error' })
  }
}
const sid = computed(() => props.session.meta.id)
const copy = (text: string) => act('Could not copy', async () => {
  await navigator.clipboard.writeText(text)
  toast.add({ title: tt('Message copied'), color: 'neutral' })
})
const rewind = (item: UserItem) => act('Could not rewind', () => store.rewind(sid.value, item.id))
// A fork from a message holds the conversation before it, with the message in the fork's composer; a queued
// message forks the whole conversation.
const fork = (text: string, at?: string) => act('Could not fork', async () => {
  await store.fork(sid.value, { at, draft: text })
  if (route.path !== '/') await navigateTo('/')
})
const cancel = (id: string) => act('Could not cancel the message', async () => {
  if (!await store.removeQueued(sid.value, id)) toast.add({ title: tt('Already sent'), description: tt('The agent has already read this message.'), color: 'neutral' })
})
const sendNow = (id: string) => act('Could not send the message', () => store.sendNow(sid.value, id))
// The turn footer: the turn's answer is the text of the assistant messages since the user message that started it.
function turnSpan(turnId: string) {
  const items = props.session.items
  const end = items.findIndex(i => i.kind === 'turn' && i.id === turnId)
  let start = end
  while (start > 0 && items[start - 1]!.kind !== 'user') start--
  return { items, start, end }
}
function answerOf(turnId: string) {
  const { items, start, end } = turnSpan(turnId)
  return items.slice(start, end).flatMap(i => i.kind === 'assistant' ? i.blocks.flatMap(b => b.type === 'text' ? [b.text] : []) : []).join('\n\n')
}
// Retry asks the turn's question again.
const retry = (turnId: string) => act('Could not retry', async () => {
  const { items, start } = turnSpan(turnId)
  const u = items[start - 1]
  if (u?.kind === 'user') await store.send(sid.value, u.text)
})
// Fork from a turn: the conversation up to it (before the next user message, or all of it).
function nextUserAfter(turnId: string) {
  const { items, end } = turnSpan(turnId)
  return items.slice(end + 1).find(i => i.kind === 'user')?.id
}
// The agent opens a page: the Browser pane shows it.
const { openPane } = usePanes()
watch(() => props.session.browserAction, (a) => {
  if (a?.phase === 'started' && a.kind === 'navigate') openPane('browser')
})
const chapters = computed(() => chaptersOf(props.session.items))
// A session started from this one: the session list catches up.
const linkedKey = computed(() => linkedSessionIds(props.session.items).filter(id => !store.sessions[id]).join())
watch(linkedKey, (k) => {
  if (k) void store.refreshList().catch(() => {})
})
// The box of the last turn (WrapUp), once the turn is over and until the user moves on.
const dismissedBoxes = useState<Record<string, boolean>>('next-step-dismissed', () => ({}))
const nextStep = computed(() => {
  if (props.session.meta.status !== 'idle') return null
  const items = props.session.items
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i]!
    if (it.kind === 'user') return null
    if (it.kind === 'tool' && it.name === 'WrapUp' && it.status === 'ok') return dismissedBoxes.value[it.id] ? null : it
  }
  return null
})
const readAloud = useReadAloud()
onBeforeUnmount(readAloud.stop)
// Read aloud automatically: a turn that finishes while the session is open (not ones loaded with it).
const autoRead = useAutoReadAloud()
const lastTurn = computed(() => props.session.items.findLast(i => i.kind === 'turn'))
watch(lastTurn, (turn, before) => {
  if (!autoRead.value || !turn || turn.kind !== 'turn' || turn.result !== 'success') return
  if (!before || before.id === turn.id || props.session.meta.id !== sid.value) return
  const text = answerOf(turn.id)
  if (text.trim()) void readAloud.toggle(turn.id, text)
})
watch(sid, () => readAloud.stop())
const edit = (id: string) => act('Could not edit the message', () => store.editQueued(sid.value, id))
// A long history mounts from the newest end, more as the view nears the top; column-reverse keeps the position.
const BLOCKS_STEP = 120
const blockLimit = ref(BLOCKS_STEP)
const showAll = useState<boolean>('transcript-show-all', () => false)
watch(() => props.session.meta.id, () => {
  blockLimit.value = BLOCKS_STEP
  showAll.value = false
})
const blocks = computed(() => showAll.value ? allBlocks.value : allBlocks.value.slice(-blockLimit.value))

// A new message (yours, a queued one or the model's next one) brings the view down to it, even when it was
// scrolled up. column-reverse puts the newest edge at scrollTop 0. Streaming into the same message does not.
const scroller = ref<HTMLElement | null>(null)
const lastKey = computed(() => {
  const last = props.session.items.findLast(i => i.kind === 'user' || i.kind === 'assistant')
  return `${props.session.meta.id}:${last?.kind}:${last?.id}:${queued.value.length}`
})
watch(lastKey, (now, before) => {
  const sameSession = now.split(':')[0] === before?.split(':')[0]
  nextTick(() => scroller.value?.scrollTo({ top: 0, behavior: sameSession ? 'smooth' : 'auto' }))
})

// column-reverse: the newest edge is scrollTop 0, scrolling up goes negative.
const scrolledUp = ref(false)
function onScroll() {
  const el = scroller.value
  scrolledUp.value = (el?.scrollTop ?? 0) < -120
  if (el && blockLimit.value < allBlocks.value.length && el.scrollHeight - el.clientHeight + el.scrollTop < 800) blockLimit.value += BLOCKS_STEP
}
function toBottom() {
  scroller.value?.scrollTo({ top: 0, behavior: 'smooth' })
}
</script>

<template>
  <div class="relative flex min-h-0 flex-col">
    <div
      ref="scroller"
      role="log"
      aria-live="off"
      :aria-label="tt('Conversation')"
      :aria-busy="session.meta.status !== 'idle'"
      class="flex min-h-0 flex-1 flex-col-reverse overflow-y-auto px-5 pt-[21px] pb-[144px]"
      @scroll="onScroll"
    >
      <SelectionMenu :root="scroller" />
      <ImageViewer :session="session" />
      <!-- The history is being replayed (first open): shown at once when it is in -->
      <div
        v-if="session.loading"
        class="m-auto"
        role="status"
        :aria-label="tt('Loading the session')"
      >
        <HMark :width="28" />
      </div>
      <div
        v-else
        class="mx-auto flex w-full max-w-[790px] flex-col gap-[15px]"
      >
        <template
          v-for="b in blocks"
          :key="b.key"
        >
          <ToolCluster
            v-if="b.kind === 'cluster'"
            :entries="b.entries"
            :session-id="session.meta.id"
          />
          <ToolGroup
            v-else-if="b.kind === 'group'"
            :tools="b.tools"
            :session-id="session.meta.id"
          />
          <VisualCard
            v-else-if="b.kind === 'visual'"
            :versions="b.versions"
            :session-id="session.meta.id"
          />
          <SentFilesCard
            v-else-if="b.kind === 'files'"
            :tool="b.tool"
            :session-id="session.meta.id"
          />
          <FindingsCard
            v-else-if="b.kind === 'findings'"
            :tool="b.tool"
            :session-id="session.meta.id"
          />
          <template v-else>
            <div
              v-if="b.item.kind === 'user'"
              class="group/msg flex flex-col gap-1"
            >
              <UserMessage
                :text="b.item.text"
                :attachments="b.item.attachments"
              />
              <MessageActions
                v-if="hasActions(b.item)"
                delivery="read"
                :at="b.item.at ?? 0"
                :busy="busy"
                :read-only="readOnly"
                :unsupported="session.meta.unsupported"
                :visible="lastUser === 'u:' + b.item.id"
                @copy="copy((b.item as UserItem).text)"
                @rewind="rewind(b.item as UserItem)"
                @fork="fork((b.item as UserItem).text, b.item.id)"
              />
            </div>
            <template v-else-if="b.item.kind === 'assistant'">
              <template
                v-for="(blk, i) in b.item.blocks"
                :key="i"
              >
                <!-- Reasoning the model kept to itself (signature only) has nothing to show. -->
                <ThinkingBlock
                  v-if="blk.type === 'thinking' && (blk.text || (b.item.streaming && i === b.item.blocks.length - 1))"
                  :text="blk.text"
                  :duration-ms="blk.durationMs"
                  :streaming="b.item.streaming && i === b.item.blocks.length - 1"
                />
                <MarkdownBlock
                  v-else
                  :text="blk.text"
                  :streaming="b.item.streaming && i === b.item.blocks.length - 1"
                />
              </template>
            </template>
            <!-- Plan mode entered: a line, like the other notices (the plan itself is a PlanCard) -->
            <div
              v-else-if="b.item.kind === 'tool' && b.item.name === 'EnterPlanMode'"
              class="flex items-center gap-2 text-[12px] text-muted"
            >
              <HIcon
                name="info"
                :size="14"
              />{{ b.item.status === 'error' ? tt('Could not enter plan mode') : b.item.status === 'running' ? tt('Entering plan mode') : tt('Entered plan mode') }}
            </div>
            <!-- A chapter: a divider, and an entry in the table of contents -->
            <template v-else-if="b.item.kind === 'tool' && b.item.name === 'MarkChapter'">
              <div
                v-if="b.item.status !== 'error' && b.item.status !== 'interrupted'"
                class="flex items-center gap-3 text-[11.5px] text-muted"
                :data-chapter="b.item.id"
                :title="String(b.item.input.summary ?? '')"
              >
                <span class="h-px flex-1 bg-line" />
                {{ String(b.item.input.title ?? '') }}
                <span class="h-px flex-1 bg-line" />
              </div>
            </template>
            <SessionCard
              v-else-if="b.item.kind === 'tool' && SESSION_CARD_TOOLS.has(b.item.name)"
              :tool="b.item"
              :session-id="session.meta.id"
              :read-only="readOnly"
            />
            <ToolCall
              v-else-if="b.item.kind === 'tool'"
              :tool="b.item"
              :session-id="session.meta.id"
            />
            <PermissionCard
              v-else-if="b.item.kind === 'permission'"
              :item="b.item"
              :session-id="session.meta.id"
            />
            <QuestionCard
              v-else-if="b.item.kind === 'question'"
              :item="b.item"
              :session-id="session.meta.id"
            />
            <BrowserSiteCard
              v-else-if="b.item.kind === 'browserSite'"
              :item="b.item"
              :session-id="session.meta.id"
            />
            <ComputerAccessCard
              v-else-if="b.item.kind === 'computerAccess'"
              :item="b.item"
              :session-id="session.meta.id"
            />
            <PlanCard
              v-else-if="b.item.kind === 'plan'"
              :item="b.item"
              :session-id="session.meta.id"
            />
            <TodoCard
              v-else-if="b.item.kind === 'todos'"
              :todos="b.item.todos"
              :subagents="session.subagents"
              :tasks="session.tasks"
            />
            <TaskCard
              v-else-if="b.item.kind === 'task' && session.tasks[b.item.taskId ?? b.item.id]"
              :task="session.tasks[b.item.taskId ?? b.item.id]!"
              :session-id="session.meta.id"
              :end="!!b.item.end"
            />
            <ShellCard
              v-else-if="b.item.kind === 'shell' && !store.inlineShells[b.item.id]"
              :item="b.item"
              :session-id="session.meta.id"
            />
            <EventCard
              v-else-if="b.item.kind === 'notice'"
              :notice="b.item"
            />
            <EventCard
              v-else-if="b.item.kind === 'hook'"
              :hook="b.item"
            />
            <EventCard
              v-else-if="b.item.kind === 'compact'"
              :compact="b.item"
            />
            <EventCard
              v-else-if="b.item.kind === 'turn'"
              :turn="b.item"
              :speaking="readAloud.speaking.value === b.item.id"
              :speech-loading="readAloud.loading.value === b.item.id"
              :no-fork="session.meta.unsupported.includes('fork')"
              @copy="copy(answerOf(b.item.id))"
              @retry="retry(b.item.id)"
              @fork="fork('', nextUserAfter(b.item.id))"
              @read-aloud="readAloud.toggle(b.item.id, answerOf(b.item.id))"
            />
            <div
              v-else-if="b.item.kind === 'rewound'"
              class="text-[12px] text-muted"
            >
              {{ tt('Rewound · {0} files restored', [b.item.filesRestored]) }}<template v-if="b.item.filesSkipped">
                {{ tt(', {0} skipped', [b.item.filesSkipped]) }}
              </template>
            </div>
          </template>
        </template>

        <div
          v-for="q in queued"
          :key="q.id"
          class="group/msg flex flex-col gap-1"
          :data-pending="q.delivery"
        >
          <UserMessage
            :text="q.text"
            :attachments="q.attachments"
            :queued="q.delivery === 'queued' || q.delivery === 'held'"
            :pending="q.delivery !== 'queued' && q.delivery !== 'held'"
            @edit="edit(q.id)"
          />
          <MessageActions
            :delivery="q.delivery"
            :at="q.at"
            :position="q.position"
            :waiting="waiting"
            :busy="busy"
            :read-only="readOnly"
            :unsupported="session.meta.unsupported"
            :visible="lastUser === 'p:' + q.id"
            @copy="copy(q.text)"
            @fork="fork(q.text)"
            @cancel="cancel(q.id)"
            @send-now="sendNow(q.id)"
          />
        </div>

        <TodoCard
          v-if="pinnedPlan"
          :todos="pinnedPlan.todos"
          :subagents="session.subagents"
          :tasks="session.tasks"
        />
        <NextStepBox
          v-if="nextStep"
          :tool="nextStep"
          :session-id="session.meta.id"
          @dismiss="dismissedBoxes[nextStep.id] = true"
        />
        <div
          v-if="working"
          class="flex items-center gap-[9px] text-[12.5px] whitespace-nowrap text-muted"
        >
          <HMark
            :width="18"
            class="shrink-0"
          />
          <span class="min-w-0 truncate"><template v-if="activity"><span class="text-strong">{{ activity }}</span> · </template>{{ tt('{0} · {1} tok', [formatDuration(now - (session.turnStartedAt || now)), formatTokens(session.turnTokens)]) }}</span>
        </div>
      </div>
    </div>
    <ChapterToc
      v-if="chapters.length"
      :chapters="chapters"
      :root="scroller"
    />
    <!-- Scrolled up: back to the newest message; the mark animates while the session is working. -->
    <button
      v-if="scrolledUp"
      type="button"
      :aria-label="working ? tt('Working · scroll to the latest') : tt('Scroll to the latest')"
      :title="working ? tt('Working · scroll to the latest') : tt('Scroll to the latest')"
      class="absolute bottom-9 left-1/2 flex h-9 -translate-x-1/2 items-center gap-2 rounded-box border border-line-strong bg-hover px-3 text-[12px] text-muted hover:text-strong"
      @click="toBottom"
    >
      <HMark
        v-if="working"
        :width="20"
      />
      <HIcon
        v-else
        name="down"
        :size="14"
      />
    </button>
  </div>
</template>
