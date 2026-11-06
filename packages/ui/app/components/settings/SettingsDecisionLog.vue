<script setup lang="ts">
// The classifier's decisions, under the audit log: what auto mode allowed or refused, what decided it and why. For admins and
// auditors; the server refuses everyone else.
import type { DecisionRecordView } from '~/lib/enterprise/types'

const store = useSessionStore()

const OUTCOMES = [
  { value: '', label: 'Every decision' },
  { value: 'deny', label: 'Refused' },
  { value: 'ask', label: 'Sent to the user' },
  { value: 'allow', label: 'Allowed' }
]

const decisions = ref<DecisionRecordView[]>([])
const shown = ref('')
const session = ref('')
const loading = ref(false)
const error = ref('')
const open = ref(false)

const rows = computed(() => shown.value ? decisions.value.filter(d => d.decision === shown.value) : decisions.value)

async function load() {
  loading.value = true
  try {
    decisions.value = (await store.transport!.enterprise().listDecisions({ sessionId: session.value.trim() })).decisions
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
  } finally {
    loading.value = false
  }
}

watch(open, (now) => {
  if (now) void load()
})
</script>

<template>
  <section class="mt-8 flex flex-col">
    <button
      type="button"
      class="flex items-center gap-2 text-left"
      :aria-expanded="open"
      @click="open = !open"
    >
      <HIcon
        :name="open ? 'chevron-down' : 'chevron-right'"
        :size="12"
        class="text-muted"
      />
      <span class="set-h !mb-0">Auto mode decisions</span>
    </button>
    <template v-if="open">
      <p class="set-sub mt-2">
        What the classifier decided about tool calls, what decided it and why. Shows the latest 100.
      </p>
      <div class="mb-3 flex gap-2 max-md:flex-col">
        <HSelect
          v-model="shown"
          aria-label="Decision"
          :options="OUTCOMES"
        />
        <input
          v-model="session"
          type="search"
          aria-label="Session id"
          placeholder="Session id"
          class="h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent"
          @keydown.enter="load"
        >
        <HBtn
          :disabled="loading"
          @click="load"
        >
          Refresh
        </HBtn>
      </div>
      <p
        v-if="error"
        class="text-[12.5px] text-error"
      >
        {{ error }}
      </p>
      <p
        v-else-if="!rows.length && !loading"
        class="text-[12.5px] text-muted"
      >
        No decisions.
      </p>
      <ul
        v-else
        class="flex flex-col"
      >
        <li
          v-for="d in rows"
          :key="d.id"
          class="border-b border-accented py-2 text-[12.5px]"
        >
          <div class="flex flex-wrap items-baseline gap-x-2 text-strong">
            <span class="font-medium">{{ d.decision }}</span>
            <span>{{ d.toolName }}</span>
            <span class="text-muted">{{ d.provider }}{{ d.cached ? ', cached' : '' }}</span>
            <span class="ml-auto text-[11.5px] text-muted">{{ new Date(d.createdAt).toLocaleString() }}</span>
          </div>
          <p
            v-if="d.reason || d.error"
            class="mt-0.5 text-toned"
          >
            {{ d.reason || d.error }}
          </p>
          <code
            v-if="d.inputPreview"
            class="mt-0.5 block truncate text-[11.5px] text-muted"
          >{{ d.inputPreview }}</code>
        </li>
      </ul>
    </template>
  </section>
</template>
