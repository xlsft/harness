<script setup lang="ts">
// Settings > Sessions > Compaction: the per-user idle compaction preference (UserPreferences, kept in the user's
// synced settings on the harness server, so it follows them to every machine). Saves at once; a failed save
// puts the toggle back.
const store = useSessionStore()

const compactIdle = ref(true)
const loaded = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

onMounted(async () => {
  if (!store.transport) return
  try {
    compactIdle.value = (await store.transport.getUserPreferences()).compactIdleSessions
    loaded.value = true
  } catch (e) {
    error.value = message(e)
  }
})

const value = computed({
  get: () => compactIdle.value,
  set: v => void save(v)
})

async function save(v: boolean) {
  if (!store.transport) return
  const prev = compactIdle.value
  compactIdle.value = v
  try {
    compactIdle.value = (await store.transport.updateUserPreferences({ compactIdleSessions: v })).compactIdleSessions
    error.value = ''
  } catch (e) {
    compactIdle.value = prev
    error.value = message(e)
  }
}
</script>

<template>
  <section
    aria-label="Compaction"
    class="mt-6 flex flex-col"
  >
    <h2 class="set-h">
      Compaction
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
      <div class="set-label">
        <span>Compact idle sessions</span>
        <span class="set-desc">After 30 minutes without activity, sessions above half their context window are compacted in the background.</span>
      </div>
      <HToggle
        v-model="value"
        label="Compact idle sessions"
        :disabled="!loaded"
      />
    </div>
  </section>
</template>
