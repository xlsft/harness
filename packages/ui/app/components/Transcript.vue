<script setup lang="ts">
// Transcript (Main.dc.html): bottom-anchored column (column-reverse keeps it pinned to the newest item),
// 790px max, 15px between items. Parallel tool calls of one message collapse into a ToolGroup.
import type { SessionState, ToolItem, TranscriptItem, UserItem } from '~/lib/session/types'
import { hasActions, pendingMessages } from '~/lib/session/delivery'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()

// Tool calls whose own card replaces them.
const HIDDEN_TOOLS = new Set(['AskUserQuestion', 'ExitPlanMode', 'TodoWrite'])

type Block
  = | { kind: 'item', key: string, item: TranscriptItem }
    | { kind: 'group', key: string, tools: ToolItem[] }
    | { kind: 'visual', key: string, versions: ToolItem[] }

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

const blocks = computed<Block[]>(() => {
  const out: Block[] = []
  for (const item of props.session.items) {
    if (item.kind === 'tool' && isVisual(item)) {
      const versions = visuals.value.get(String(item.input.id ?? item.id))!
      if (versions[versions.length - 1] === item) out.push({ kind: 'visual', key: 'v:' + String(item.input.id ?? item.id), versions })
      continue
    }
    if (item.kind === 'tool') {
      if (HIDDEN_TOOLS.has(item.name)) continue
      if (item.grouped) {
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
  return out
})

// Messages not read by the agent yet: handed to the client (sending, sent), in the client's queue, then in this
// browser's queue (lib/session/delivery.ts).
const queued = computed(() => pendingMessages(props.session, store.localQueue[props.session.meta.id], store.outbox[props.session.meta.id]))
const waiting = computed(() => queued.value.filter(q => q.delivery === 'queued').length)
const working = computed(() => props.session.meta.status === 'working')
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
  toast.add({ title: 'Message copied', color: 'neutral' })
})
const rewind = (item: UserItem) => act('Could not rewind', () => store.rewind(sid.value, item.id))
// A fork from a message holds the conversation before it, with the message in the fork's composer; a queued
// message forks the whole conversation.
const fork = (text: string, at?: string) => act('Could not fork', async () => {
  await store.fork(sid.value, { at, draft: text })
  if (route.path !== '/') await navigateTo('/')
})
const cancel = (id: string) => act('Could not cancel the message', async () => {
  if (!await store.removeQueued(sid.value, id)) toast.add({ title: 'Already sent', description: 'The agent has already read this message.', color: 'neutral' })
})
const sendNow = (id: string) => act('Could not send the message', () => store.sendNow(sid.value, id))
const edit = (id: string) => act('Could not edit the message', () => store.editQueued(sid.value, id))
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

const lastIsStreaming = computed(() => {
  const last = props.session.items[props.session.items.length - 1]
  return last?.kind === 'assistant' && last.streaming
})
</script>

<template>
  <div
    ref="scroller"
    class="flex flex-col-reverse overflow-y-auto px-5 pt-[21px] pb-3"
  >
    <SelectionMenu :root="scroller" />
    <div class="mx-auto flex w-full max-w-[790px] flex-col gap-[15px]">
      <template
        v-for="b in blocks"
        :key="b.key"
      >
        <ToolGroup
          v-if="b.kind === 'group'"
          :tools="b.tools"
          :session-id="session.meta.id"
        />
        <VisualCard
          v-else-if="b.kind === 'visual'"
          :versions="b.versions"
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
          <PlanCard
            v-else-if="b.item.kind === 'plan'"
            :item="b.item"
            :session-id="session.meta.id"
          />
          <TodoCard
            v-else-if="b.item.kind === 'todos'"
            :todos="b.item.todos"
          />
          <TaskCard
            v-else-if="b.item.kind === 'task' && session.tasks[b.item.id]"
            :task="session.tasks[b.item.id]!"
            :session-id="session.meta.id"
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
            v-else-if="b.item.kind === 'compact'"
            :compact="b.item"
          />
          <EventCard
            v-else-if="b.item.kind === 'turn'"
            :turn="b.item"
          />
          <div
            v-else-if="b.item.kind === 'rewound'"
            class="text-[12px] text-muted"
          >
            Rewound · {{ b.item.filesRestored }} files restored<template v-if="b.item.filesSkipped">
              , {{ b.item.filesSkipped }} skipped
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
          :queued="q.delivery === 'queued'"
          :pending="q.delivery !== 'queued'"
          @edit="edit(q.id)"
        />
        <MessageActions
          :delivery="q.delivery"
          :at="q.at"
          :position="q.position"
          :waiting="waiting"
          :busy="busy"
          :read-only="readOnly"
          :visible="lastUser === 'p:' + q.id"
          @copy="copy(q.text)"
          @fork="fork(q.text)"
          @cancel="cancel(q.id)"
          @send-now="sendNow(q.id)"
        />
      </div>

      <div
        v-if="working && !lastIsStreaming && !session.items.some(i => i.kind === 'task' && session.tasks[i.id]?.state === 'running' && !session.tasks[i.id]?.tail)"
        class="flex items-center gap-[9px] text-[12.5px] text-muted"
      >
        <HIcon
          name="loading"
          spin
          class="text-accent"
        />{{ session.todos.find(t => t.status === 'in_progress')?.activeForm || 'Thinking' }}
      </div>
    </div>
  </div>
</template>
