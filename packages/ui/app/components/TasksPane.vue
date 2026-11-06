<script setup lang="ts">
// Background tasks pane (docs/design/ui/TasksPane.dc.html), on real session data: running tasks are
// cards (the first one open, each toggles with its chevron), finished runs are rows with ✓ / ✕.
import { usePanes } from '~/composables/usePanes'
import type { SessionState, TaskVM, WorkflowAgentVM, WorkflowVM } from '~/lib/session/types'
import { taskPid } from '~/lib/fixtures/tasks'
import { taskGraphCounts, taskNodeLine } from '~/lib/session/teamwork'

const props = defineProps<{ session: SessionState, menu: string }>()

const { openPane, closePane } = usePanes()
const store = useSessionStore()
const now = useNow()

const cleared = ref<string[]>([])
const all = computed(() => props.session.taskOrder.map(id => props.session.tasks[id]).filter((t): t is TaskVM => !!t))
const running = computed(() => all.value.filter(t => t.state === 'running'))
const finished = computed(() => all.value.filter(t => t.state !== 'running' && !cleared.value.includes(t.id)))
const finishedOpen = ref(true)
const workflows = computed(() => props.session.workflowOrder.map(id => props.session.workflows[id]).filter((w): w is WorkflowVM => !!w && !cleared.value.includes(w.id)))

// A run's agents by phase: its phases in order, then agents named no phase.
function phaseGroups(w: WorkflowVM) {
  const groups = w.phases.map(title => ({ title, agents: w.agents.filter(a => a.phase === title) }))
  const rest = w.agents.filter(a => !w.phases.includes(a.phase))
  return rest.length ? [{ title: '', agents: rest }, ...groups] : groups
}

// The live state of a run's agent, when its own event is newer.
function agentState(a: WorkflowAgentVM) {
  return a.cached ? 'cached' : (props.session.subagents[a.agentId]?.state ?? a.state)
}

function stopWorkflow(w: WorkflowVM) {
  store.stopWorkflow(props.session.meta.id, w.id)
}

const workflowLine = (w: WorkflowVM) => [w.state === 'running' ? w.currentPhase : w.state, `${w.agents.length} agents`].filter(Boolean).join(' · ')

// Open/closed per task; untouched cards follow the default (only the first running card is open).
const toggled = ref<Record<string, boolean>>({})
function isOpen(t: TaskVM) {
  return toggled.value[t.id] ?? (t.state === 'running' && t.id === running.value[0]?.id)
}
function toggle(t: TaskVM) {
  toggled.value[t.id] = !isOpen(t)
}

function home(path: string) {
  return path.replace(/^\/(?:Users|home)\/[^/]+/, '~')
}

function meta(t: TaskVM) {
  const port = t.tail.match(/localhost:(\d+)/)?.[1]
  return [home(props.session.meta.cwd), `pid ${taskPid(t.id)}`, port ? `:${port}` : ''].filter(Boolean).join(' · ')
}

function tail(t: TaskVM) {
  return t.tail.replace(/\n+$/, '').split('\n').slice(-8).join('\n')
}

// 0.4s · 1.2s · 4s · 48s · 1m 12s
function took(ms: number) {
  if (ms < 10_000 && ms % 1000 !== 0) return `${(ms / 1000).toFixed(1)}s`
  return formatDuration(ms)
}

function stop(t: TaskVM) {
  store.stopTask(props.session.meta.id, t.id)
}

function clearFinished() {
  cleared.value = [...cleared.value, ...finished.value.map(t => t.id), ...workflows.value.filter(w => w.state !== 'running').map(w => w.id)]
}
</script>

<template>
  <section
    :aria-label="tt('Background tasks')"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="font-display text-[13px] text-strong">{{ tt('Background tasks') }}</span>
      <span class="flex-1" />
      <PaneBtn
        icon="trash"
        :label="tt('Clear finished')"
        @click="clearFinished"
      />
      <PaneFullBtn />
      <PaneBtn
        icon="close"
        :label="tt('Close')"
        @click="closePane('tasks')"
      />
    </div>

    <div
      v-if="!running.length && !finished.length && !workflows.length && !session.taskGraph.length"
      class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
    >
      <HIcon
        name="term"
        :size="24"
        class="text-muted"
      />
      <span class="text-strong">{{ tt('No background tasks') }}</span>
      <span class="max-w-[320px] text-[12px] leading-[1.55] text-muted">{{ tt('Dev servers, watchers, long test runs, monitors and workflows the agent starts in the background show up here.') }}</span>
    </div>

    <div
      v-else
      class="flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto p-3"
    >
      <template v-if="workflows.length">
        <div class="px-[2px] pt-[2px] pb-1 text-[12px] text-muted">
          {{ tt('Workflows') }} <span class="text-strong">{{ workflows.length }}</span>
        </div>
        <div
          v-for="w in workflows"
          :key="w.id"
          class="shrink-0 border border-line bg-surface"
        >
          <div class="flex h-[38px] items-center gap-[9px] pr-2 pl-2.5 text-[12.5px]">
            <HIcon
              :name="w.state === 'running' ? 'loading' : w.state === 'finished' ? 'check' : 'x'"
              :size="13"
              :spin="w.state === 'running'"
              :class="w.state === 'running' ? 'text-accent' : w.state === 'finished' ? 'text-ok' : w.state === 'failed' ? 'text-bad' : 'text-muted'"
            />
            <span class="min-w-0 flex-1 truncate text-strong">{{ w.name }}</span>
            <span class="text-[11.5px] whitespace-nowrap text-muted">{{ workflowLine(w) }}</span>
            <button
              v-if="w.state === 'running'"
              type="button"
              :aria-label="tt('Stop')"
              :title="tt('Stop')"
              class="flex size-6 shrink-0 items-center justify-center border border-line bg-raised hover:border-line-strong"
              @click="stopWorkflow(w)"
            >
              <span class="size-2 bg-strong" />
            </button>
          </div>
          <div class="flex flex-col gap-1 border-t border-line px-2.5 pt-2 pb-2.5 text-[11.5px]">
            <span class="text-muted">{{ w.description }}</span>
            <span
              v-if="w.error"
              class="whitespace-pre-wrap text-bad"
            >{{ w.error }}</span>
            <template
              v-for="g in phaseGroups(w)"
              :key="g.title"
            >
              <span
                v-if="g.title"
                :class="g.title === w.currentPhase && w.state === 'running' ? 'text-strong' : 'text-toned'"
              >{{ g.title }}</span>
              <span
                v-for="(a, i) in g.agents"
                :key="i"
                class="flex items-center gap-1.5 pl-2.5 text-muted"
              >
                <span class="min-w-0 flex-1 truncate">{{ a.label }}</span>
                <span class="whitespace-nowrap">{{ agentState(a) }}</span>
              </span>
            </template>
            <pre
              v-if="w.log.length"
              class="m-0 border border-muted bg-default px-2 py-1.5 text-[11.5px] leading-[1.6] whitespace-pre-wrap text-muted"
            >{{ w.log.slice(-8).join('\n') }}</pre>
          </div>
        </div>
      </template>
      <div class="px-[2px] pt-[2px] pb-1 text-[12px] text-muted">
        {{ tt('Running') }} <span class="text-strong">{{ running.length }}</span>
      </div>
      <div
        v-for="t in running"
        :key="t.id"
        class="shrink-0 border border-line bg-surface"
      >
        <div
          class="flex h-[38px] cursor-pointer items-center gap-[9px] pr-2 pl-2.5 text-[12.5px]"
          @click="toggle(t)"
        >
          <HIcon
            :name="isOpen(t) ? 'down' : 'right'"
            :size="12"
            class="text-muted"
          />
          <HIcon
            name="loading"
            :size="13"
            spin
            class="text-accent"
          />
          <span class="min-w-0 flex-1 truncate text-strong">{{ t.command }}</span>
          <span class="text-[11.5px] whitespace-nowrap text-muted">{{ formatDuration(now - t.startedAt) }}</span>
          <button
            type="button"
            :aria-label="tt('Stop')"
            :title="tt('Stop')"
            class="flex size-6 shrink-0 items-center justify-center border border-line bg-raised hover:border-line-strong"
            @click.stop="stop(t)"
          >
            <span class="size-2 bg-strong" />
          </button>
        </div>
        <div
          v-if="isOpen(t)"
          class="flex flex-col gap-1.5 border-t border-line pt-2 pr-2.5 pb-2.5 pl-[31px] text-[11.5px]"
        >
          <span class="text-muted">{{ meta(t) }}</span>
          <pre
            v-if="t.tail"
            class="m-0 border border-muted bg-default px-2 py-1.5 text-[11.5px] leading-[1.6] whitespace-pre-wrap text-muted"
          >{{ tail(t) }}</pre>
          <div class="flex gap-1.5">
            <button
              type="button"
              class="border border-line px-2 py-px text-[11px] text-strong hover:bg-hover"
              @click="openPane('terminal')"
            >
              {{ tt('Open in terminal') }}
            </button>
            <button
              type="button"
              class="border border-line px-2 py-px text-[11px] text-strong hover:bg-hover"
            >
              {{ tt('Send output to chat') }}
            </button>
          </div>
        </div>
      </div>

      <div
        v-if="session.taskGraph.length"
        data-task-graph
        class="flex flex-col gap-1 px-3 py-2 text-[12px]"
      >
        <span class="text-[11.5px] font-medium text-muted">{{ tt('Task graph · {0} of {1} done', [taskGraphCounts(session.taskGraph).done, taskGraphCounts(session.taskGraph).total]) }}</span>
        <div
          v-for="t in session.taskGraph"
          :key="t.id"
          class="flex items-baseline gap-2"
          :class="(t.status === 'done' || t.status === 'cancelled') && 'opacity-60'"
        >
          <span class="shrink-0 text-[11px] text-muted">{{ t.id }}</span>
          <span
            class="min-w-0 flex-1 truncate text-toned"
            :title="t.note || t.description"
          >{{ t.title }}</span>
          <span
            class="shrink-0 text-[11px]"
            :class="t.status === 'failed' ? 'text-bad' : t.status === 'in_progress' ? 'text-accent' : t.status === 'done' ? 'text-ok' : 'text-muted'"
          >{{ taskNodeLine(t) }}</span>
        </div>
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
          />{{ tt('Finished') }} <span class="text-strong">{{ finished.length }}</span>
        </button>
        <template v-if="finishedOpen">
          <div
            v-for="t in finished"
            :key="t.id"
            class="shrink-0 border border-muted bg-surface"
          >
            <div
              class="flex h-[34px] cursor-pointer items-center gap-[9px] px-2.5 text-[12.5px]"
              @click="toggle(t)"
            >
              <HIcon
                :name="isOpen(t) ? 'down' : 'right'"
                :size="12"
                class="text-muted"
              />
              <HIcon
                :name="t.state === 'done' ? 'check' : 'x'"
                :size="13"
                :class="t.state === 'done' ? 'text-ok' : t.state === 'failed' ? 'text-bad' : 'text-muted'"
              />
              <span class="min-w-0 flex-1 truncate text-toned">{{ t.command }}</span>
              <span
                v-if="t.state === 'failed'"
                class="text-[11px] whitespace-nowrap text-bad"
              >{{ tt('exit {0}', [t.exitCode]) }}</span>
              <span
                v-else-if="t.state === 'stopped'"
                class="text-[11px] whitespace-nowrap text-muted"
              >{{ tt('stopped') }}</span>
              <span class="text-[11.5px] whitespace-nowrap text-muted">{{ took(t.durationMs) }}</span>
            </div>
            <div
              v-if="isOpen(t)"
              class="flex flex-col gap-1.5 border-t border-muted pt-2 pr-2.5 pb-2.5 pl-[31px] text-[11.5px]"
            >
              <span class="text-muted">{{ home(session.meta.cwd) }}{{ t.outputPath ? ` · ${t.outputPath}` : '' }}</span>
              <pre
                v-if="t.tail"
                class="m-0 border border-muted bg-default px-2 py-1.5 text-[11.5px] leading-[1.6] whitespace-pre-wrap text-muted"
              >{{ tail(t) }}</pre>
            </div>
          </div>
        </template>
      </template>
    </div>
  </section>
</template>
