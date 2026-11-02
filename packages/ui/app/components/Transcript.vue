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

const blocks = computed<Block[]>(() => {
  const out: Block[] = []
  for (const item of props.session.items) {
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

const queued = computed(() => store.localQueue[props.session.meta.id] ?? [])
const working = computed(() => props.session.meta.status === 'working')
const lastIsStreaming = computed(() => {
  const last = props.session.items[props.session.items.length - 1]
  return last?.kind === 'assistant' && last.streaming
})
</script>

<template>
  <div class="flex flex-col-reverse overflow-y-auto px-5 pt-[21px] pb-3">
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
              <ThinkingBlock
                v-if="blk.type === 'thinking'"
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
