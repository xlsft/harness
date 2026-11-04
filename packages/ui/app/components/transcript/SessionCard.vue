<script setup lang="ts">
// The session tools in the transcript: a started session as a row that opens it, a proposal as a card the user may start
// or dismiss, and the rows of SessionList. An offer's question is a QuestionCard of its own; this only adds the outcome.
import { sessionCardOf } from '~/lib/session/links'
import type { ToolItem } from '~/lib/session/types'

const props = defineProps<{ tool: ToolItem, sessionId: string, readOnly?: boolean }>()
const store = useSessionStore()
const toast = useToast()
const card = computed(() => sessionCardOf(props.tool, store.sessions[props.sessionId]?.proposals ?? {}))
const busy = ref(false)
async function resolve(action: 'start' | 'dismiss') {
  const c = card.value
  if (c.kind !== 'proposal' || !c.proposal) return
  busy.value = true
  try {
    await store.resolveProposal(props.sessionId, c.proposal.id, action)
  } catch (err) {
    toast.add({ title: action === 'start' ? 'Could not start the session' : 'Could not dismiss the proposal', description: (err instanceof Error ? err.message : String(err)).replace(/^\[\w+\]\s*/, ''), color: 'error' })
  } finally {
    busy.value = false
  }
}
const STATE_TEXT = { dismissed: 'Dismissed', expired: 'Expired' } as const
</script>

<template>
  <SessionRow
    v-if="card.kind === 'session'"
    :id="card.id"
    :name="card.name"
    :title="card.title"
  />
  <div
    v-else-if="card.kind === 'list'"
    class="flex flex-col gap-1"
  >
    <SessionRow
      v-for="r in card.rows"
      :id="r.id"
      :key="r.id"
      :name="r.name"
      :title="r.title"
      :state="r.state"
    />
    <span
      v-if="!card.rows.length"
      class="text-[12px] text-muted"
    >No sessions</span>
  </div>
  <div
    v-else-if="card.kind === 'proposal'"
    class="flex flex-col gap-2 border border-default px-3.5 py-3 text-[12.5px]"
    data-session-proposal
  >
    <span class="text-strong">{{ card.title }}</span>
    <p class="m-0 line-clamp-3 leading-[1.55] whitespace-pre-wrap text-muted">
      {{ card.prompt }}
    </p>
    <SessionRow
      v-if="card.proposal?.state === 'started'"
      :id="card.proposal.sessionId"
      :name="card.proposal.sessionName"
      :title="card.title"
    />
    <div
      v-else-if="card.proposal?.state === 'pending' && !readOnly"
      class="flex gap-2"
    >
      <HBtn
        size="md"
        variant="primary"
        :disabled="busy"
        @click="resolve('start')"
      >
        Start session
      </HBtn>
      <HBtn
        size="md"
        variant="outline"
        :disabled="busy"
        @click="resolve('dismiss')"
      >
        Dismiss
      </HBtn>
    </div>
    <span
      v-else
      class="text-[12px] text-muted"
    >{{ card.proposal && card.proposal.state !== 'pending' && card.proposal.state !== 'started' ? STATE_TEXT[card.proposal.state] : 'Not available here' }}</span>
  </div>
  <div
    v-else-if="card.kind === 'status'"
    class="flex items-center gap-2 text-[12px]"
    :class="card.error ? 'text-warn' : 'text-muted'"
  >
    <HIcon
      :name="card.error ? 'alert' : 'info'"
      :size="14"
    />{{ card.text }}
  </div>
</template>
