<script setup lang="ts">
// Transcript (Main.dc.html): bottom-anchored column (column-reverse keeps it pinned to the newest item),
// 790px max, 15px between items. Parallel tool calls of one message collapse into a ToolGroup.
import type { SessionState, ToolItem, TranscriptItem } from '~/lib/session/types'

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

// Messages waiting in this browser, then the ones sent to the client while a question waits for its answer.
const queued = computed(() => [...(store.localQueue[props.session.meta.id] ?? []), ...props.session.queue])
const working = computed(() => props.session.meta.status === 'working')
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
          <UserMessage
            v-if="b.item.kind === 'user'"
            :text="b.item.text"
            :attachments="b.item.attachments"
          />
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

      <UserMessage
        v-for="q in queued"
        :key="q.id"
        :text="q.text"
        queued
      />

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
