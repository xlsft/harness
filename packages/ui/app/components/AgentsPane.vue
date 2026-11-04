<script setup lang="ts">
// Agents pane (docs/design/ui/AgentsPane.dc.html): the session's subagents, from the client's SubagentUpdated events.
// A running agent shows its current tool and latest steps; any agent opens to its transcript.
import { usePanes } from '~/composables/usePanes'
import type { GetSubagentTranscriptResponse } from '~/gen/harness/agent/v1/subagents_pb'
import type { SessionState, SubagentStep, SubagentVM } from '~/lib/session/types'
import type { IconName } from '~/utils/icons'

const props = defineProps<{ session: SessionState, menu: string }>()

const { closePane } = usePanes()
const store = useSessionStore()
const now = useNow()

const cleared = ref<string[]>([])
const agents = computed(() => props.session.subagentOrder.map(id => props.session.subagents[id]!))
const running = computed(() => agents.value.filter(a => a.state === 'running' || a.state === 'queued'))
const finished = computed(() => agents.value.filter(a => a.state !== 'running' && a.state !== 'queued' && !cleared.value.includes(a.id)))
const finishedOpen = ref(true)

const STEP: Record<SubagentStep['status'], { icon: IconName, color: string, fg: string }> = {
  done: { icon: 'check', color: 'text-ok', fg: 'text-muted' },
  running: { icon: 'loading', color: 'text-accent', fg: 'text-strong' },
  waiting: { icon: 'dot', color: 'text-line-strong', fg: 'text-muted' },
  failed: { icon: 'x', color: 'text-bad', fg: 'text-toned' }
}

function clearFinished() {
  cleared.value = [...cleared.value, ...finished.value.map(a => a.id)]
}

function stop(a: SubagentVM) {
  store.stopSubagent(props.session.meta.id, a.id)
}

// The agent whose transcript is open.
const openId = ref('')
const transcript = ref<GetSubagentTranscriptResponse['items']>([])
const transcriptError = ref('')
async function toggleTranscript(a: SubagentVM) {
  if (openId.value === a.id) {
    openId.value = ''
    return
  }
  openId.value = a.id
  transcript.value = []
  transcriptError.value = ''
  await loadTranscript(a.id)
}
async function loadTranscript(id: string) {
  try {
    const items = await store.subagentTranscript(props.session.meta.id, id)
    if (openId.value === id) transcript.value = items
  } catch (e) {
    if (openId.value === id) transcriptError.value = e instanceof Error ? e.message : String(e)
  }
}
// Follows the open agent while it works.
const openAgent = computed(() => props.session.subagents[openId.value])
watch(() => [openAgent.value?.toolCalls, openAgent.value?.state], () => {
  if (openId.value) loadTranscript(openId.value)
})

function elapsed(a: SubagentVM) {
  return formatDuration((a.finishedAt || now.value) - a.startedAt)
}
</script>

<template>
  <section
    aria-label="Agents"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="font-display text-[13px] text-strong">Agents</span>
      <span class="flex-1" />
      <PaneBtn
        icon="trash"
        label="Clear finished"
        @click="clearFinished"
      />
      <PaneBtn
        icon="full"
        label="Maximize"
      />
      <PaneBtn
        icon="close"
        label="Close"
        @click="closePane('agents')"
      />
    </div>

    <div
      v-if="!running.length && !finished.length"
      class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
    >
      <HIcon
        name="ai"
        :size="24"
        class="text-muted"
      />
      <span class="text-strong">No agents running</span>
      <span class="max-w-[320px] text-[12px] leading-[1.55] text-muted">Subagents started from this session show up here with their own tasks.</span>
    </div>

    <div
      v-else
      class="flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto p-3"
    >
      <div class="px-[2px] pt-[2px] pb-1 text-[12px] text-muted">
        Running <span class="text-strong">{{ running.length }}</span>
      </div>
      <div
        v-for="a in running"
        :key="a.id"
        class="shrink-0 border border-line bg-surface"
      >
        <div class="flex h-[38px] items-center gap-2 pr-2 pl-2.5 text-[12.5px]">
          <button
            type="button"
            :aria-expanded="openId === a.id"
            aria-label="Transcript"
            class="flex min-w-0 flex-1 items-center gap-2 text-left"
            @click="toggleTranscript(a)"
          >
            <HIcon
              :name="openId === a.id ? 'down' : 'right'"
              :size="12"
              class="text-muted"
            />
            <HIcon
              name="ai"
              :size="13"
              class="text-accent"
            />
            <span class="min-w-0 truncate text-strong">{{ a.name }}</span>
            <span class="shrink-0 text-[11px] text-muted">{{ a.type }}</span>
          </button>
          <span class="text-[11px] whitespace-nowrap text-muted">{{ a.state === 'queued' ? 'queued' : a.currentTool || a.model }}</span>
          <span class="text-[11.5px] whitespace-nowrap text-muted">{{ elapsed(a) }}</span>
          <button
            type="button"
            aria-label="Stop agent"
            title="Stop agent"
            class="flex size-6 shrink-0 items-center justify-center border border-line bg-raised hover:border-line-strong"
            @click="stop(a)"
          >
            <span class="size-2 bg-strong" />
          </button>
        </div>
        <div
          v-if="a.steps.length"
          class="flex flex-col border-t border-muted py-1.5 pr-2.5 pl-[29px]"
        >
          <div class="flex h-[22px] items-center text-[12px] text-muted">
            {{ a.toolCalls }} tool calls
          </div>
          <div
            v-for="s in a.steps"
            :key="s.id"
            class="flex h-[26px] items-center gap-2 border-l border-line pl-2.5 text-[12px]"
          >
            <HIcon
              :name="STEP[s.status].icon"
              :size="12"
              :spin="s.status === 'running'"
              :class="STEP[s.status].color"
            />
            <span
              class="min-w-0 flex-1 truncate"
              :class="STEP[s.status].fg"
            >{{ s.tool }}{{ s.target ? ` ${s.target}` : '' }}</span>
          </div>
        </div>
        <AgentTranscript
          v-if="openId === a.id"
          :items="transcript"
          :error="transcriptError"
        />
      </div>

      <template v-if="finished.length">
        <button
          type="button"
          :aria-expanded="finishedOpen"
          class="flex items-center gap-1.5 px-[2px] pt-3.5 pb-1 text-left text-[12px] text-muted"
          @click="finishedOpen = !finishedOpen"
        >
          <HIcon
            :name="finishedOpen ? 'down' : 'right'"
            :size="11"
          />Finished <span class="text-strong">{{ finished.length }}</span>
        </button>
        <template v-if="finishedOpen">
          <div
            v-for="a in finished"
            :key="a.id"
            class="shrink-0 border border-muted bg-surface"
          >
            <button
              type="button"
              :aria-expanded="openId === a.id"
              class="flex h-[34px] w-full items-center gap-2 px-2.5 text-left text-[12.5px]"
              @click="toggleTranscript(a)"
            >
              <HIcon
                :name="openId === a.id ? 'down' : 'right'"
                :size="12"
                class="text-muted"
              />
              <HIcon
                :name="a.state === 'finished' ? 'check' : 'x'"
                :size="13"
                :class="a.state === 'finished' ? 'text-ok' : 'text-bad'"
              />
              <span class="min-w-0 flex-1 truncate text-toned">{{ a.name }}</span>
              <span
                v-if="a.state !== 'finished'"
                class="text-[11px] text-muted"
              >{{ a.state }}</span>
              <span class="text-[11px] whitespace-nowrap text-muted">{{ a.toolCalls }} tool calls</span>
              <span class="text-[11.5px] whitespace-nowrap text-muted">{{ elapsed(a) }}</span>
            </button>
            <AgentTranscript
              v-if="openId === a.id"
              :items="transcript"
              :error="transcriptError"
            />
          </div>
        </template>
      </template>
    </div>
  </section>
</template>
