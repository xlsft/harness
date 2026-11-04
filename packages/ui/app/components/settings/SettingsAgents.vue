<script setup lang="ts">
// Settings > Sessions > Agents: the limits on subagents (UserPreferences, kept in the user's synced settings). The
// client validates and reads them when an agent starts; a refused value puts the field back and says why.
const store = useSessionStore()

const saved = ref({ subagentMaxDepth: 2, subagentMaxRunning: 8 })
const depth = ref(2)
const running = ref(8)
const loaded = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function apply(p: { subagentMaxDepth: number, subagentMaxRunning: number }) {
  saved.value = { subagentMaxDepth: p.subagentMaxDepth, subagentMaxRunning: p.subagentMaxRunning }
  depth.value = p.subagentMaxDepth
  running.value = p.subagentMaxRunning
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
  const patch = { subagentMaxDepth: Number(depth.value), subagentMaxRunning: Number(running.value) }
  if (patch.subagentMaxDepth === saved.value.subagentMaxDepth && patch.subagentMaxRunning === saved.value.subagentMaxRunning) return
  try {
    apply(await store.transport.updateUserPreferences(patch))
    error.value = ''
  } catch (e) {
    apply(saved.value)
    error.value = message(e)
  }
}
</script>

<template>
  <section
    aria-label="Agents"
    class="mt-6 flex flex-col"
  >
    <h2 class="set-h">
      Agents
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
        <span>Nesting depth</span>
        <span class="set-desc">How many levels of agents a session may start, from 1 to 5. At 2, its agents may start agents of their own, and those may not.</span>
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
        <span>Agents running at once</span>
        <span class="set-desc">How many agents of a session work at the same time, from 1 to 32. The others wait for a free slot.</span>
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
  </section>
</template>
