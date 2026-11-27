<script setup lang="ts">
// Above the composer while a session has a goal: the condition, the turns taken of the cap, and what the last check said.
// The client runs the goal; the buttons send `/goal stop` and `/goal resume`, as the user could type them. Also above the
// composer: where a moved session went, what became of a host's work, and a best-of-N attempt's test result.
import type { SessionState } from '~/lib/session/types'
import { goalStateText } from '~/lib/session/teamwork'
import { attemptCheckText } from '~/lib/hosts/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const goal = computed(() => props.session.goal)
const dismissed = ref('')
const key = computed(() => (goal.value ? `${goal.value.startedAt}:${goal.value.state}` : ''))
const running = computed(() => goal.value?.state === 'active')
const resumable = computed(() => goal.value?.state === 'paused' || goal.value?.state === 'cap_reached')
const shown = computed(() => !!goal.value && goal.value.state !== 'stopped' && (running.value || dismissed.value !== key.value))
const send = (text: string) => store.quietly(store.send(props.session.meta.id, text))
const hostWork = computed(() => props.session.hostWork)

// A session that moved to an always-on host is read-only here; the link follows the work row to the host's session.
const moved = computed(() => props.session.meta.moved)
const toast = useToast()
async function openMoved() {
  const m = moved.value
  if (!m?.workId || !store.transport) return
  try {
    const work = await store.transport.hosts().get(m.workId)
    if (!work.sessionId) throw new Error(tt('The machine has not started it yet'))
    await store.refreshList()
    store.open(work.sessionId)
  } catch (e) {
    toast.add({ title: tt('Could not open it on {0}', [m.hostName]), description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
  }
}
// An attempt of a best-of-N group: its number, its last test result, and the way to the comparison.
const attempt = computed(() => props.session.meta.attempt)
const attemptsView = useAttemptsView()
</script>

<template>
  <div
    v-if="moved"
    role="status"
    data-moved
    class="flex items-center gap-2 border border-default bg-surface px-3 py-1.5 text-[12px] text-muted"
  >
    <span class="min-w-0 truncate">{{ tt('Moved to {0} on {1}: read-only here', [moved.hostName, moved.branch]) }}</span>
    <button
      type="button"
      class="text-accent"
      @click="openMoved"
    >
      {{ tt('Open on {0}', [moved.hostName]) }}
    </button>
  </div>
  <div
    v-if="attempt"
    role="status"
    data-attempt
    class="flex items-center gap-2 border border-default bg-surface px-3 py-1.5 text-[12px] text-muted"
  >
    <span>{{ tt('Attempt {0} of {1}', [attempt.index + 1, attempt.count || '?']) }}</span>
    <span
      v-if="session.attemptCheck"
      :class="session.attemptCheck.exitCode === 0 && !session.attemptCheck.refused ? 'text-ok' : 'text-bad'"
    >{{ attemptCheckText(session.attemptCheck) }}</span>
    <button
      type="button"
      class="ml-auto text-accent"
      @click="attemptsView = attempt.groupId"
    >
      {{ tt('Compare attempts') }}
    </button>
  </div>
  <div
    v-if="hostWork"
    role="status"
    data-host-work
    class="flex items-center gap-2 border border-default bg-surface px-3 py-1.5 text-[12px] text-muted"
  >
    <template v-if="hostWork.error">
      <span class="text-bad">{{ tt('The machine could not push this work: {0}', [hostWork.error]) }}</span>
    </template>
    <template v-else>
      <span class="min-w-0 truncate">{{ tt('Pushed to {0}', [hostWork.branch]) }}</span>
      <a
        v-if="hostWork.pullRequestUrl"
        :href="hostWork.pullRequestUrl"
        target="_blank"
        rel="noopener"
        class="text-accent"
      >{{ tt('Pull request') }}</a>
    </template>
  </div>
  <div
    v-if="goal && shown"
    role="status"
    data-goal
    class="flex items-start gap-2.5 border border-default bg-surface px-3 py-2.5 text-[12.5px]"
  >
    <div class="flex min-w-0 flex-1 flex-col gap-0.5">
      <span class="text-strong">{{ tt('{0} · turn {1} of {2}', [goalStateText(goal), goal.turns, goal.maxTurns]) }}</span>
      <span class="truncate text-toned">{{ goal.condition }}</span>
      <span
        v-if="goal.reason"
        class="text-[11.5px] text-muted"
      >{{ goal.reason }}</span>
    </div>
    <HBtn
      v-if="resumable"
      @click="send('/goal resume')"
    >
      {{ tt('Resume') }}
    </HBtn>
    <HBtn
      v-if="running || resumable"
      @click="send('/goal stop')"
    >
      {{ tt('Stop') }}
    </HBtn>
    <HBtn
      v-if="!running && !resumable"
      variant="ghost"
      @click="dismissed = key"
    >
      {{ tt('Dismiss') }}
    </HBtn>
  </div>
</template>
