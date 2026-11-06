<script setup lang="ts">
// Row under a user message: its delivery state with a relative time on the left (queued with its place in the
// queue, sending, one check when the client accepted it, two checks once the agent read it), then the actions
// (lib/session/delivery.ts decides which). The transcript shows the row on hover, and always on the last message.
import { messageActions, type Delivery } from '~/lib/session/delivery'

const props = defineProps<{
  delivery: Delivery
  // ms; 0 when unknown.
  at: number
  // Place among the waiting messages and how many wait (the place shows when more than one does).
  position?: number
  waiting?: number
  busy: boolean
  readOnly: boolean
  visible: boolean
  // What the session's engine cannot do (SessionMeta.unsupported).
  unsupported?: string[]
}>()
const emit = defineEmits<{ copy: [], rewind: [], fork: [], cancel: [], sendNow: [] }>()

const now = useNow()
const actions = computed(() => messageActions(props.delivery, { busy: props.busy, readOnly: props.readOnly, unsupported: props.unsupported }))
const label = computed(() => {
  switch (props.delivery) {
    case 'queued': return (props.waiting ?? 0) > 1 && props.position ? `queued · ${props.position} of ${props.waiting}` : 'queued'
    case 'held': return 'held until limits return'
    case 'sending': return 'sending'
    case 'sent': return 'sent'
    default: return 'read'
  }
})
const ago = computed(() => props.at ? formatAgo(now.value - props.at) : '')
const title = computed(() => props.at ? new Date(props.at).toLocaleString() : undefined)

const buttons = computed(() => [
  { key: 'sendNow', icon: 'arrow-right' as const, label: tt('Send now'), state: actions.value.sendNow, hint: tt('Stop the turn and send this now') },
  { key: 'cancel', icon: 'close' as const, label: tt('Cancel'), state: actions.value.cancel, hint: tt('Remove from the queue') },
  { key: 'copy', icon: 'copy' as const, label: tt('Copy'), state: actions.value.copy, hint: tt('Copy the message') },
  { key: 'rewind', icon: 'history' as const, label: tt('Rewind'), state: actions.value.rewind, hint: actions.value.rewindHint || 'Rewind code and conversation to before this message' },
  { key: 'fork', icon: 'branch' as const, label: tt('Fork'), state: actions.value.fork, hint: tt('Fork into a sub-session from here') }
].filter(b => b.state !== 'hidden'))

function run(key: string) {
  switch (key) {
    case 'copy': return emit('copy')
    case 'rewind': return emit('rewind')
    case 'fork': return emit('fork')
    case 'cancel': return emit('cancel')
    case 'sendNow': return emit('sendNow')
  }
}
</script>

<template>
  <div
    class="flex h-6 items-center justify-end gap-1 self-end text-[11px] text-muted transition-opacity"
    :class="visible ? 'opacity-100' : 'pointer-events-none opacity-0 group-hover/msg:pointer-events-auto group-hover/msg:opacity-100 group-focus-within/msg:pointer-events-auto group-focus-within/msg:opacity-100'"
    data-message-actions
  >
    <span
      class="mr-1 inline-flex items-center gap-1"
      :title="title"
      :data-delivery="delivery"
    >
      <HIcon
        v-if="delivery === 'queued' || delivery === 'held' || delivery === 'sending'"
        name="clock"
        :size="12"
      />
      <HIcon
        v-else-if="delivery === 'sent'"
        name="check"
        :size="12"
      />
      <span
        v-else
        class="relative inline-flex w-[17px] text-accent"
        aria-hidden="true"
      >
        <HIcon
          name="check"
          :size="12"
        />
        <HIcon
          name="check"
          :size="12"
          class="absolute left-[5px]"
        />
      </span>
      <span
        v-if="delivery === 'read' || delivery === 'sent'"
        class="sr-only"
      >{{ label }}</span>
      <span v-else>{{ label }}</span>
      <span v-if="ago">{{ delivery !== 'read' && delivery !== 'sent' ? '·' : '' }} {{ ago }}</span>
    </span>
    <UTooltip
      v-for="b in buttons"
      :key="b.key"
      :text="b.hint"
      :delay-duration="500"
      :content="{ side: 'bottom', sideOffset: 4 }"
    >
      <button
        type="button"
        :aria-label="b.label"
        :disabled="b.state === 'disabled'"
        :data-action="b.key"
        class="flex size-6 items-center justify-center text-muted hover:bg-hover hover:text-strong disabled:cursor-default disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-muted"
        @click="run(b.key)"
      >
        <HIcon
          :name="b.icon"
          :size="13"
        />
      </button>
    </UTooltip>
  </div>
</template>
