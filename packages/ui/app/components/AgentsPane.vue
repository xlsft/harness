<script setup lang="ts">
// Agents pane (docs/design/ui/AgentsPane.dc.html): subagents from the transcript (Task / Agent tool
// calls). A running agent that reported steps or tool calls is a group with a "{n} tasks" row and
// an expandable step list (the first group starts open); finished agents are ✓ / ✕ rows.
import { usePanes } from '~/composables/usePanes'
import type { SessionState, SubagentStep, ToolItem } from '~/lib/session/types'
import type { IconName } from '~/utils/icons'

const props = defineProps<{ session: SessionState, menu: string }>()

const { closePane } = usePanes()
const now = useNow()

const cleared = ref<string[]>([])
const agents = computed(() => props.session.items.filter((i): i is ToolItem => i.kind === 'tool' && (i.name === 'Task' || i.name === 'Agent')))
const running = computed(() => agents.value.filter(a => a.status === 'running'))
const finished = computed(() => agents.value.filter(a => a.status !== 'running' && !cleared.value.includes(a.id)))
const finishedOpen = ref(true)

function title(a: ToolItem) {
  return String(a.input.description ?? a.summary ?? a.name)
}

function isGroup(a: ToolItem) {
  return !!a.subagent && (a.subagent.steps.length > 0 || a.subagent.toolCalls > 0)
}

const firstGroup = computed(() => running.value.find(isGroup)?.id)
const toggled = ref<Record<string, boolean>>({})
function isOpen(a: ToolItem) {
  return toggled.value[a.id] ?? a.id === firstGroup.value
}
function toggle(a: ToolItem) {
  toggled.value[a.id] = !isOpen(a)
}

const STEP: Record<SubagentStep['status'], { icon: IconName, color: string, fg: string }> = {
  done: { icon: 'check', color: 'text-ok', fg: 'text-muted' },
  running: { icon: 'loading', color: 'text-accent', fg: 'text-white' },
  waiting: { icon: 'dot', color: 'text-line-strong', fg: 'text-muted' },
  failed: { icon: 'x', color: 'text-bad', fg: 'text-toned' }
}

function clearFinished() {
  cleared.value = [...cleared.value, ...finished.value.map(a => a.id)]
}
</script>

<template>
  <section
    aria-label="Agents"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="font-display text-[13px] text-white">Agents</span>
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
      <span class="text-white">No agents running</span>
      <span class="max-w-[320px] text-[12px] leading-[1.55] text-muted">Subagents and parallel sessions started from this session show up here with their own tasks.</span>
    </div>

    <div
      v-else
      class="flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto p-3"
    >
      <div class="px-[2px] pt-[2px] pb-1 text-[12px] text-muted">
        Running <span class="text-white">{{ running.length }}</span>
      </div>
      <div
        v-for="a in running"
        :key="a.id"
        class="shrink-0 border border-line bg-surface"
      >
        <div class="flex h-[38px] items-center gap-2 pr-2 pl-2.5 text-[12.5px]">
          <HIcon
            v-if="!isGroup(a)"
            name="right"
            :size="12"
            class="text-muted"
          />
          <span
            v-else
            class="size-3 shrink-0"
          />
          <HIcon
            name="ai"
            :size="13"
            class="text-accent"
          />
          <span class="min-w-0 truncate text-white">{{ title(a) }}</span>
          <HIcon
            name="right"
            :size="12"
            :class="isGroup(a) ? 'text-muted' : 'text-transparent'"
          />
          <span class="flex-1" />
          <span class="text-[11px] whitespace-nowrap text-muted">{{ a.subagent?.model }}</span>
          <span class="text-[11.5px] whitespace-nowrap text-muted">{{ formatDuration(now - a.startedAt) }}</span>
          <button
            type="button"
            aria-label="Stop agent"
            title="Stop agent"
            class="flex size-6 shrink-0 items-center justify-center border border-line bg-raised hover:border-line-strong"
          >
            <span class="size-2 bg-white" />
          </button>
        </div>
        <template v-if="isGroup(a)">
          <button
            type="button"
            :aria-expanded="isOpen(a)"
            class="flex h-8 w-full items-center gap-2 border-t border-muted px-2.5 text-left text-[12px] text-muted"
            @click="toggle(a)"
          >
            <HIcon
              :name="isOpen(a) ? 'down' : 'right'"
              :size="11"
            />{{ a.subagent?.toolCalls }} tasks<span class="flex-1" />
            <span
              v-if="a.subagent?.active"
              class="text-accent"
            >{{ a.subagent.active }} running</span>
          </button>
          <div
            v-if="isOpen(a) && a.subagent?.steps.length"
            class="flex flex-col pr-2.5 pb-2 pl-[29px]"
          >
            <div
              v-for="(s, i) in a.subagent.steps"
              :key="i"
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
              <span class="text-[11px] whitespace-nowrap text-muted">{{ s.meta }}</span>
            </div>
          </div>
        </template>
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
          />Finished <span class="text-white">{{ finished.length }}</span>
        </button>
        <template v-if="finishedOpen">
          <div
            v-for="a in finished"
            :key="a.id"
            class="shrink-0 border border-muted bg-surface"
          >
            <div class="flex h-[34px] items-center gap-2 px-2.5 text-[12.5px]">
              <HIcon
                v-if="!a.subagent?.toolCalls"
                name="right"
                :size="12"
                class="text-muted"
              />
              <span
                v-else
                class="size-3 shrink-0"
              />
              <HIcon
                :name="a.status === 'ok' ? 'check' : 'x'"
                :size="13"
                :class="a.status === 'ok' ? 'text-ok' : 'text-bad'"
              />
              <span class="min-w-0 flex-1 truncate text-toned">{{ title(a) }}</span>
              <span class="text-[11.5px] whitespace-nowrap text-muted">{{ formatDuration(a.durationMs) }}</span>
            </div>
            <div
              v-if="a.subagent?.toolCalls"
              class="flex h-[30px] items-center gap-2 border-t border-muted px-2.5 text-[12px] text-muted"
            >
              <HIcon
                name="right"
                :size="11"
              />{{ a.subagent.toolCalls }} tasks
            </div>
          </div>
        </template>
      </template>
    </div>
  </section>
</template>
