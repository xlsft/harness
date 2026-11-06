<script setup lang="ts">
// Settings > Sessions > Pull requests: how often a watched pull request may wake a session (UserPreferences, kept in
// the user's synced settings). The client validates; a refused value puts the field back and says why.
const store = useSessionStore()

const wakes = ref(30)
const draft = ref(30)
const loaded = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

onMounted(async () => {
  if (!store.transport) return
  try {
    wakes.value = draft.value = (await store.transport.getUserPreferences()).prMonitorWakesPerHour
    loaded.value = true
  } catch (e) {
    error.value = message(e)
  }
})

async function save() {
  if (!store.transport || draft.value === wakes.value) return
  try {
    wakes.value = draft.value = (await store.transport.updateUserPreferences({ prMonitorWakesPerHour: Number(draft.value) })).prMonitorWakesPerHour
    error.value = ''
  } catch (e) {
    draft.value = wakes.value
    error.value = message(e)
  }
}
</script>

<template>
  <section
    aria-label="Pull requests"
    class="mt-6 flex flex-col"
  >
    <h2 class="set-h">
      Pull requests
    </h2>
    <p class="set-sub">
      Saved to your account on the server, so it applies on every machine.
    </p>
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
        for="pr-wakes-per-hour"
      >
        <span>Auto-fix wakes per hour</span>
        <span class="set-desc">How many times per hour a watched pull request may wake a session, from 1 to 240. What is over the limit waits.</span>
      </label>
      <input
        id="pr-wakes-per-hour"
        v-model.number="draft"
        type="number"
        min="1"
        max="240"
        step="1"
        class="w-20 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded"
        @change="save"
      >
    </div>
  </section>
</template>
