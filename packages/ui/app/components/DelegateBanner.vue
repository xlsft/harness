<script setup lang="ts">
// Above the composer while this session has delegated work (/delegate, or a lead session it started): how far the
// helpers are, what it has cost, and the tasks with who works on each. The client runs the delegation; the buttons send
// `/delegate pause|resume|stop`, as the user could type them; "Open lead" and a helper's row open those sessions.
import type { SessionState } from '~/lib/session/types'
import { TaskNodeStatus } from '~/gen/harness/agent/v1/teamwork_pb'
import { ProjectRunState, ProjectWorkerState } from '~/gen/harness/agent/v1/project_runs_pb'
import { activeWorkers, budgetText, runEnded } from '~/lib/inbox/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const run = computed(() => props.session.delegation)
const expanded = ref(false)
const confirmStop = ref(false)
const dismissed = ref('')
const ended = computed(() => !!run.value && runEnded(run.value.state))
const shown = computed(() => !!run.value && !(ended.value && dismissed.value === run.value.id))
const paused = computed(() => run.value?.state === ProjectRunState.PAUSED)
const send = (text: string) => store.quietly(store.send(props.session.meta.id, text))
// The lead is another session when this one started it; when this session leads, there is nothing to open.
const lead = computed(() => (run.value && run.value.coordinatorSessionId !== props.session.meta.id ? run.value.coordinatorSessionId : ''))

const tasks = computed(() => {
  const r = run.value
  if (!r) return []
  return r.tasks.map((t) => {
    const w = [...r.workers].reverse().find(x => x.taskId === t.id)
    return { id: t.id, title: t.title, status: t.status, worker: w }
  })
})
const doneCount = computed(() => tasks.value.filter(t => t.status === TaskNodeStatus.DONE).length)

// What a task's row says, in the words of someone who handed it out.
function taskState(t: (typeof tasks.value)[number]): { text: string, tone: string } {
  const w = t.worker
  if (t.status === TaskNodeStatus.DONE) return { text: w?.state === ProjectWorkerState.MERGED ? tt('done, merged') : tt('done'), tone: 'text-ok' }
  if (!w) return { text: t.status === TaskNodeStatus.IN_PROGRESS ? tt('in progress') : tt('waiting its turn'), tone: 'text-muted' }
  switch (w.state) {
    case ProjectWorkerState.STARTING: return { text: tt('starting'), tone: 'text-muted' }
    case ProjectWorkerState.RUNNING: return { text: tt('working'), tone: 'text-accent' }
    case ProjectWorkerState.NEEDS_INPUT: return { text: tt('needs you'), tone: 'text-warn' }
    case ProjectWorkerState.FAILED: return { text: tt('failed'), tone: 'text-bad' }
    case ProjectWorkerState.STOPPED:
    case ProjectWorkerState.INTERRUPTED: return { text: tt('stopped'), tone: 'text-muted' }
    default: return { text: tt('finished'), tone: 'text-ok' }
  }
}

const headline = computed(() => {
  const r = run.value
  if (!r) return ''
  switch (r.state) {
    case ProjectRunState.PLANNING: return tt('Delegating: splitting the work into tasks')
    case ProjectRunState.WAITING_FOR_USER: return tt('Delegated work waits for your answer')
    case ProjectRunState.MERGING: return tt('Delegated work: putting the results together')
    case ProjectRunState.PAUSED: return r.reason ? tt('Delegation paused: {0}', [r.reason]) : tt('Delegation paused')
    case ProjectRunState.DONE: return tt('Delegated work is done')
    case ProjectRunState.FAILED: return tt('Delegated work failed')
    case ProjectRunState.STOPPED: return tt('Delegation stopped')
  }
  return tt('Delegated to helpers')
})
const progress = computed(() => {
  const r = run.value
  if (!r) return ''
  const parts = []
  if (tasks.value.length) parts.push(tt('{0} of {1} tasks done', [doneCount.value, tasks.value.length]))
  const working = activeWorkers(r)
  if (working) parts.push(tt('{0} working', [working]))
  parts.push(budgetText(r))
  return parts.join(' · ')
})

function stop() {
  if (!confirmStop.value) {
    confirmStop.value = true
    setTimeout(() => (confirmStop.value = false), 4000)
    return
  }
  confirmStop.value = false
  send('/delegate stop')
}
async function openHelper(sessionId: string) {
  await store.refreshList()
  store.open(sessionId)
}
</script>

<template>
  <div
    v-if="shown && run"
    role="status"
    data-delegation
    class="flex flex-col border border-default bg-surface text-[12px]"
  >
    <div class="flex items-center gap-2 px-3 py-1.5">
      <HIcon
        name="branch"
        :size="13"
        class="shrink-0 text-muted"
      />
      <span class="min-w-0 truncate text-strong">{{ headline }}</span>
      <span class="min-w-0 truncate text-muted max-sm:hidden">{{ progress }}</span>
      <span class="ml-auto flex shrink-0 items-center gap-3">
        <button
          v-if="lead"
          type="button"
          class="text-muted hover:text-strong"
          @click="openHelper(lead)"
        >
          {{ tt('Open lead') }}
        </button>
        <button
          v-if="tasks.length"
          type="button"
          class="text-muted hover:text-strong"
          :aria-expanded="expanded"
          @click="expanded = !expanded"
        >
          {{ expanded ? tt('Hide tasks') : tt('Tasks') }}
        </button>
        <template v-if="!ended">
          <button
            type="button"
            class="text-accent"
            @click="send(paused ? '/delegate resume' : '/delegate pause')"
          >
            {{ paused ? tt('Resume') : tt('Pause') }}
          </button>
          <button
            type="button"
            class="text-bad"
            @click="stop"
          >
            {{ confirmStop ? tt('Stop all helpers?') : tt('Stop') }}
          </button>
        </template>
        <button
          v-else
          type="button"
          class="text-muted hover:text-strong"
          @click="dismissed = run.id"
        >
          {{ tt('Dismiss') }}
        </button>
      </span>
    </div>
    <p
      v-if="ended && run.reason"
      class="m-0 border-t border-default px-3 py-1.5 text-muted"
    >
      {{ run.reason }}
    </p>
    <ul
      v-if="expanded"
      class="m-0 list-none border-t border-default p-0"
    >
      <li
        v-for="t in tasks"
        :key="t.id"
        class="flex items-center gap-2 px-3 py-1"
      >
        <span class="min-w-0 flex-1 truncate text-toned">{{ t.title }}</span>
        <span
          class="shrink-0"
          :class="taskState(t).tone"
        >{{ taskState(t).text }}</span>
        <button
          v-if="t.worker?.sessionId && !t.worker.remote"
          type="button"
          class="shrink-0 text-accent"
          @click="openHelper(t.worker.sessionId)"
        >
          {{ tt('Open') }}
        </button>
        <span
          v-else-if="t.worker?.deviceName"
          class="shrink-0 text-muted"
        >{{ t.worker.deviceName }}</span>
      </li>
    </ul>
  </div>
</template>
