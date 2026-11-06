<script setup lang="ts">
// Above the composer while a session has a goal: the condition, the turns taken of the cap, and what the last check said.
// The client runs the goal; the buttons send `/goal stop` and `/goal resume`, as the user could type them.
import type { SessionState } from '~/lib/session/types'
import { goalStateText } from '~/lib/session/teamwork'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const goal = computed(() => props.session.goal)
const dismissed = ref('')
const key = computed(() => (goal.value ? `${goal.value.startedAt}:${goal.value.state}` : ''))
const running = computed(() => goal.value?.state === 'active')
const resumable = computed(() => goal.value?.state === 'paused' || goal.value?.state === 'cap_reached')
const shown = computed(() => !!goal.value && goal.value.state !== 'stopped' && (running.value || dismissed.value !== key.value))
const send = (text: string) => store.quietly(store.send(props.session.meta.id, text))
</script>

<template>
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
