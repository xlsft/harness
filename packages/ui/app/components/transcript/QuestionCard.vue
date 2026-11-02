<script setup lang="ts">
// AskUserQuestion (Event.dc.html "question"): options as radio rows (checkboxes for multi-select),
// Answer / Other…, one question at a time ("2 of 3"). Resolved by QuestionResolved.
import type { QuestionItem } from '~/lib/session/types'

const props = defineProps<{ item: QuestionItem, sessionId: string }>()
const store = useSessionStore()
const step = ref(0)
const answers = ref<string[][]>(props.item.items.map(q => q.options[0] ? [q.options[0].label] : []))
const other = ref('')
const otherOpen = ref(false)

const q = computed(() => props.item.items[step.value]!)

function toggle(label: string) {
  const cur = answers.value[step.value] ?? []
  answers.value[step.value] = q.value.multiSelect
    ? cur.includes(label) ? cur.filter(l => l !== label) : [...cur, label]
    : [label]
}

function submit() {
  if (otherOpen.value && other.value.trim()) answers.value[step.value] = [other.value.trim()]
  otherOpen.value = false
  other.value = ''
  if (step.value < props.item.items.length - 1) {
    step.value += 1
    return
  }
  store.answerQuestion(props.sessionId, props.item.id, answers.value)
}

const split = (label: string) => {
  const m = label.match(/^(.*?)\s*(\(recommended\))$/)
  return m ? { main: m[1], hint: m[2] } : { main: label, hint: '' }
}
</script>

<template>
  <div
    v-if="item.state === 'pending'"
    class="border border-default bg-surface"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        name="question"
        class="text-accent"
      />
      <span class="text-white">{{ q.question }}</span>
      <span class="flex-1" />
      <span
        v-if="item.items.length > 1"
        class="text-[11px] text-muted"
      >{{ step + 1 }} of {{ item.items.length }}</span>
    </div>
    <div class="flex flex-col gap-0.5 p-1.5 text-[12.5px]">
      <label
        v-for="o in q.options"
        :key="o.label"
        class="flex cursor-default gap-2.5 border-l-2 px-2 py-[7px]"
        :class="answers[step]?.includes(o.label) ? 'border-accent bg-hover' : 'border-transparent'"
      >
        <input
          :type="q.multiSelect ? 'checkbox' : 'radio'"
          :name="item.id"
          :checked="answers[step]?.includes(o.label)"
          class="mt-[3px] accent-accent"
          @change="toggle(o.label)"
        >
        <span class="flex flex-col">
          <span class="text-white">{{ split(o.label).main }} <span
            v-if="split(o.label).hint"
            class="text-muted"
          >{{ split(o.label).hint }}</span></span>
          <span class="text-[11.5px] text-muted">{{ o.description }}</span>
        </span>
      </label>
      <input
        v-if="otherOpen"
        v-model="other"
        autofocus
        placeholder="Type your own answer"
        class="mx-2 my-1 border border-default bg-default px-2.5 py-1.5 text-white outline-none placeholder:text-muted focus:border-accent"
        @keydown.enter="submit"
      >
    </div>
    <div class="flex gap-2 border-t border-default px-3 py-2">
      <HBtn
        variant="primary"
        size="md"
        @click="submit"
      >
        Answer
      </HBtn>
      <HBtn
        size="md"
        @click="otherOpen = !otherOpen"
      >
        Other…
      </HBtn>
    </div>
  </div>
  <div
    v-else
    class="flex items-center gap-[9px] text-[12px] text-muted"
  >
    <HIcon
      name="question"
      :size="14"
      class="text-ok"
    />Answered<span class="truncate text-toned">{{ item.items[0]?.question }}</span>
  </div>
</template>
