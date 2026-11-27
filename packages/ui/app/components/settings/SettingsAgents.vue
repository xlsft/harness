<script setup lang="ts">
// Settings > Sessions > Agents: the limits on subagents and on best-of-N attempts (UserPreferences, kept in the user's synced
// settings). The client validates and reads them when an agent starts; a refused value puts the field back and says why. The
// managed policy may cap the attempts lower; the server applies that.
const store = useSessionStore()

const saved = ref({ subagentMaxDepth: 2, subagentMaxRunning: 8, workflowMaxAgents: 30, bestOfNParallel: 5, bestOfNBudgetUsd: 0 })
const depth = ref(2)
const running = ref(8)
const perWorkflow = ref(30)
const attemptsAtOnce = ref(5)
const attemptBudget = ref(0)
const loaded = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function apply(p: { subagentMaxDepth: number, subagentMaxRunning: number, workflowMaxAgents: number, bestOfNParallel: number, bestOfNBudgetUsd: number }) {
  saved.value = { subagentMaxDepth: p.subagentMaxDepth, subagentMaxRunning: p.subagentMaxRunning, workflowMaxAgents: p.workflowMaxAgents, bestOfNParallel: p.bestOfNParallel || 5, bestOfNBudgetUsd: p.bestOfNBudgetUsd }
  attemptsAtOnce.value = p.bestOfNParallel || 5
  attemptBudget.value = p.bestOfNBudgetUsd
  depth.value = p.subagentMaxDepth
  running.value = p.subagentMaxRunning
  perWorkflow.value = p.workflowMaxAgents
}

onMounted(async () => {
  if (!store.transport) return
  try {
    apply(await store.transport.getUserPreferences())
    loaded.value = true
  } catch (e) {
    error.value = message(e)
  }
})

async function save() {
  if (!store.transport) return
  const patch = { subagentMaxDepth: Number(depth.value), subagentMaxRunning: Number(running.value), workflowMaxAgents: Number(perWorkflow.value), bestOfNParallel: Number(attemptsAtOnce.value), bestOfNBudgetUsd: Number(attemptBudget.value) }
  const before = saved.value
  if (patch.subagentMaxDepth === before.subagentMaxDepth && patch.subagentMaxRunning === before.subagentMaxRunning && patch.workflowMaxAgents === before.workflowMaxAgents && patch.bestOfNParallel === before.bestOfNParallel && patch.bestOfNBudgetUsd === before.bestOfNBudgetUsd) return
  try {
    apply(await store.transport.updateUserPreferences(patch))
    error.value = ''
  } catch (e) {
    apply({ ...saved.value })
    error.value = message(e)
  }
}
</script>

<template>
  <section
    :aria-label="tt('Agents')"
    class="mt-6 flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Agents') }}
    </h2>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <div class="set-row">
      <label
        class="set-label"
        for="subagent-max-depth"
      >
        <span>{{ tt('Nesting depth') }}</span>
        <span class="set-desc">{{ tt('How many levels of agents a session may start, from 1 to 5. At 2, its agents may start agents of their own, and those may not.') }}</span>
      </label>
      <input
        id="subagent-max-depth"
        v-model.number="depth"
        type="number"
        min="1"
        max="5"
        step="1"
        class="w-20 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded"
        @change="save"
      >
    </div>
    <div class="set-row">
      <label
        class="set-label"
        for="subagent-max-running"
      >
        <span>{{ tt('Agents running at once') }}</span>
        <span class="set-desc">{{ tt('How many agents of a session work at the same time, from 1 to 32. The others wait for a free slot.') }}</span>
      </label>
      <input
        id="subagent-max-running"
        v-model.number="running"
        type="number"
        min="1"
        max="32"
        step="1"
        class="w-20 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded"
        @change="save"
      >
    </div>
    <div class="set-row">
      <label
        class="set-label"
        for="workflow-max-agents"
      >
        <span>{{ tt('Agents per workflow') }}</span>
        <span class="set-desc">{{ tt('The most agents one run of a workflow may call, from 1 to 200. Past it, the script gets an error.') }}</span>
      </label>
      <input
        id="workflow-max-agents"
        v-model.number="perWorkflow"
        type="number"
        min="1"
        max="200"
        step="1"
        class="w-20 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded"
        @change="save"
      >
    </div>
    <div class="set-row">
      <label
        class="set-label"
        for="best-of-n-parallel"
      >
        <span>{{ tt('Attempts working at once') }}</span>
        <span class="set-desc">{{ tt('Best of N: how many attempts of a group work at the same time, from 1 to 5. The others wait.') }}</span>
      </label>
      <input
        id="best-of-n-parallel"
        v-model.number="attemptsAtOnce"
        type="number"
        min="1"
        max="5"
        step="1"
        class="w-20 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded"
        @change="save"
      >
    </div>
    <div class="set-row">
      <label
        class="set-label"
        for="best-of-n-budget"
      >
        <span>{{ tt('Budget per attempt') }}</span>
        <span class="set-desc">{{ tt('Best of N: what one attempt may spend, in USD; 0 for no limit of your own. An attempt that reaches it stops.') }}</span>
      </label>
      <input
        id="best-of-n-budget"
        v-model.number="attemptBudget"
        type="number"
        min="0"
        max="1000"
        step="0.5"
        class="w-20 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded"
        @change="save"
      >
    </div>
  </section>
</template>
