<script setup lang="ts">
// AskUserQuestion (Event.dc.html "question"): one card for 1–4 questions, a step each ("2 / 3"), one submit at
// the end; Back goes to the previous step before submitting. Single / yes_no are radio rows, multi checkboxes
// (min..max), text a free field; "Other…" adds free text unless the model disabled it; recommended options
// come first, highlighted. Skip hands the decision back to the model; "Answer with message" closes the question
// with the chat messages written meanwhile. Resolved by QuestionResolved (answered, skipped, message, cancelled).
import type { QuestionItem } from '~/lib/session/types'
import {
  answerSummary,
  draftAnswer,
  draftValid,
  emptyDraft,
  orderedOptions,
  toggleOption,
  toggleOther,
  type QuestionDraft
} from '~/lib/session/question'

const props = defineProps<{ item: QuestionItem, sessionId: string }>()
const store = useSessionStore()
// The answers so far and the step, kept while the card is away (another session open) or the page reloads.
const { draft: saved, clear } = useCardDraft(`question:${props.sessionId}:${props.item.id}`, () => ({ step: 0, drafts: props.item.items.map(() => emptyDraft()) as QuestionDraft[] }))
const step = computed({ get: () => saved.value.step, set: (v: number) => (saved.value.step = v) })
const drafts = computed(() => saved.value.drafts)
watch(() => props.item.state, s => s !== 'pending' && clear())
const sending = ref(false)
const error = ref('')

const q = computed(() => props.item.items[step.value]!)
const d = computed(() => drafts.value[step.value]!)
const last = computed(() => step.value === props.item.items.length - 1)
const options = computed(() => orderedOptions(q.value))
const multi = computed(() => q.value.type === 'multi')
const valid = computed(() => draftValid(q.value, d.value))
const empty = computed(() => draftAnswer(q.value, d.value).skipped)
const canChat = computed(() => store.hasChatAnswer(props.sessionId))
const chatCount = computed(() => store.sessions[props.sessionId]?.queue.length ?? 0)

const bounds = computed(() => {
  const { min, max } = q.value
  if (!multi.value) return ''
  if (min === 0) return `choose up to ${max}`
  return min === max ? `choose ${min}` : `choose ${min} to ${max}`
})

function set(next: QuestionDraft) {
  drafts.value[step.value] = next
}

async function run(f: () => Promise<void>) {
  if (sending.value) return
  sending.value = true
  error.value = ''
  try {
    await f()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    sending.value = false
  }
}

function next() {
  if (!valid.value) return
  if (!last.value) {
    step.value += 1
    return
  }
  const answers = props.item.items.map((it, i) => draftAnswer(it, drafts.value[i]!))
  run(() => store.answerQuestion(props.sessionId, props.item.id, answers))
}

const skip = () => run(() => store.skipQuestion(props.sessionId, props.item.id))
const withMessage = () => run(() => store.answerWithMessage(props.sessionId, props.item.id))

const resolved = computed(() => ({
  pending: { text: '', cls: '' },
  answered: { text: 'Answered', cls: 'text-ok' },
  skipped: { text: 'Skipped · left to the agent', cls: 'text-muted' },
  message: { text: 'Answered with a message', cls: 'text-ok' },
  cancelled: { text: 'Question cancelled', cls: 'text-muted' }
}[props.item.state]))
</script>

<template>
  <div
    v-if="item.state === 'pending'"
    class="border border-default bg-surface"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        name="question"
        class="shrink-0 text-accent"
      />
      <span class="shrink-0 border border-default px-[5px] text-[11px] text-muted">{{ q.header }}</span>
      <span class="min-w-0 text-strong">{{ q.question }}</span>
      <span class="flex-1" />
      <span
        v-if="item.items.length > 1"
        class="shrink-0 text-[11px] text-muted"
      >{{ step + 1 }} / {{ item.items.length }}</span>
    </div>

    <div class="flex flex-col gap-0.5 p-1.5 text-[12.5px]">
      <span
        v-if="bounds"
        class="px-2 pt-0.5 pb-1 text-[11px] text-muted"
      >{{ bounds }}</span>
      <label
        v-for="o in options"
        :key="o.label"
        class="flex cursor-pointer gap-2.5 border-l-2 px-2 py-[7px]"
        :class="d.selected.includes(o.label) ? 'border-accent bg-hover' : 'border-transparent'"
      >
        <input
          :type="multi ? 'checkbox' : 'radio'"
          :name="item.id + ':' + step"
          :checked="d.selected.includes(o.label)"
          class="mt-[3px] accent-accent"
          @change="set(toggleOption(q, d, o.label))"
        >
        <span class="flex flex-col">
          <span class="text-strong">{{ o.label }} <span
            v-if="o.recommended"
            class="text-accent"
          >{{ tt('recommended') }}</span></span>
          <span
            v-if="o.description"
            class="text-[11.5px] text-muted"
          >{{ o.description }}</span>
        </span>
      </label>
      <label
        v-if="q.allowOther"
        class="flex cursor-pointer gap-2.5 border-l-2 px-2 py-[7px]"
        :class="d.otherOpen ? 'border-accent bg-hover' : 'border-transparent'"
      >
        <input
          :type="multi ? 'checkbox' : 'radio'"
          :name="item.id + ':' + step"
          :checked="d.otherOpen"
          class="mt-[3px] accent-accent"
          @change="set(toggleOther(q, d))"
        >
        <span class="text-strong">{{ tt('Other…') }}</span>
      </label>
      <input
        v-if="q.type === 'text' || d.otherOpen"
        :key="step"
        :value="d.other"
        autofocus
        :placeholder="q.type === 'text' ? tt('Type your answer') : tt('Type your own answer')"
        class="mx-2 my-1 border border-default bg-default px-2.5 py-1.5 text-strong outline-none placeholder:text-muted focus:border-accent"
        @input="set({ ...d, other: ($event.target as HTMLInputElement).value })"
        @keydown.enter.prevent="next"
      >
    </div>

    <div
      v-if="chatCount"
      class="border-t border-default px-3 py-1.5 text-[11.5px] text-muted"
    >
      {{ tt('{0} chat message{1} written meanwhile will follow your answer, or send them as the answer.', [chatCount, chatCount > 1 ? 's' : '']) }}
    </div>
    <div class="flex flex-wrap items-center gap-2 border-t border-default px-3 py-2">
      <HBtn
        v-if="step > 0"
        size="md"
        :disabled="sending"
        @click="step -= 1"
      >
        {{ tt('Back') }}
      </HBtn>
      <HBtn
        variant="primary"
        size="md"
        :disabled="sending || !valid || (empty && item.items.length === 1)"
        @click="next"
      >
        {{ last ? (item.items.length > 1 ? tt('Submit') : tt('Answer')) : (empty ? tt('Skip question') : tt('Next')) }}
      </HBtn>
      <HBtn
        size="md"
        :disabled="sending"
        @click="skip"
      >
        {{ tt('Skip') }}
      </HBtn>
      <span class="flex-1" />
      <HBtn
        v-if="canChat"
        size="md"
        :disabled="sending"
        @click="withMessage"
      >
        {{ tt('Answer with message') }}
      </HBtn>
    </div>
    <div
      v-if="error"
      class="border-t border-default px-3 py-1.5 text-[11.5px] text-bad"
    >
      {{ error }}
    </div>
  </div>
  <div
    v-else
    class="flex flex-col gap-0.5 text-[12px] text-muted"
  >
    <div class="flex items-center gap-[9px]">
      <HIcon
        name="question"
        :size="14"
        :class="resolved.cls"
      />
      <span :class="resolved.cls">{{ resolved.text }}</span>
      <span
        v-if="item.state !== 'answered' || item.items.length === 1"
        class="truncate text-toned"
      >{{ item.items[0]?.question }}</span>
      <span class="flex-1" />
      <span
        v-if="item.resolvedBy && item.resolvedBy !== 'local' && item.resolvedBy !== 'history'"
        class="shrink-0"
      >{{ tt('by {0}', [item.resolvedBy]) }}</span>
    </div>
    <template v-if="item.state === 'answered'">
      <div
        v-for="(it, i) in item.items"
        :key="i"
        class="flex gap-[9px] pl-[23px]"
      >
        <span class="shrink-0">{{ it.header }}</span>
        <span class="truncate text-toned">{{ answerSummary(it, item.answers[i]) }}</span>
      </div>
    </template>
    <div
      v-else-if="item.state === 'message' && item.message"
      class="truncate pl-[23px] text-toned"
    >
      {{ item.message }}
    </div>
  </div>
</template>
