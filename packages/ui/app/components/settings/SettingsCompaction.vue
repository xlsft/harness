<script setup lang="ts">
// Settings > Sessions > Context: per-user preferences about what fills a session's context (UserPreferences, kept in
// the user's synced settings on the harness server, so they follow them to every machine): idle compaction, and the
// deferral of rarely used built-in tool definitions, which the organization's policy may force. Saves at once; a
// failed save puts the toggle back.
const store = useSessionStore()

const compactIdle = ref(true)
const deferTools = ref(true)
// The policy's choice when it forces one (null: the user chooses).
const deferForced = ref<boolean | null>(null)
const loaded = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

onMounted(async () => {
  if (!store.transport) return
  try {
    const p = await store.transport.getUserPreferences()
    compactIdle.value = p.compactIdleSessions
    deferTools.value = p.deferBuiltinTools
    deferForced.value = p.managedPolicy?.deferBuiltinTools ?? null
    loaded.value = true
  } catch (e) {
    error.value = message(e)
  }
})

const value = computed({
  get: () => compactIdle.value,
  set: v => void save(v)
})

const deferValue = computed({
  get: () => deferForced.value ?? deferTools.value,
  set: v => void saveDefer(v)
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

async function saveDefer(v: boolean) {
  if (!store.transport || deferForced.value !== null) return
  const prev = deferTools.value
  deferTools.value = v
  try {
    deferTools.value = (await store.transport.updateUserPreferences({ deferBuiltinTools: v })).deferBuiltinTools
    error.value = ''
  } catch (e) {
    deferTools.value = prev
    error.value = message(e)
  }
}
</script>

<template>
  <section
    :aria-label="tt('Context')"
    class="mt-6 flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Context') }}
    </h2>
    <p class="set-sub">
      {{ tt('Saved to your account on the server, so it applies on every machine.') }}
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
        <span>{{ tt('Compact idle sessions') }}</span>
        <span class="set-desc">{{ tt('After 30 minutes without activity, sessions above half their context window are compacted in the background.') }}</span>
      </div>
      <HToggle
        v-model="value"
        :label="tt('Compact idle sessions')"
        :disabled="!loaded"
      />
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>{{ tt('Load rarely used tools on demand') }}</span>
        <span class="set-desc">{{ tt('Browser, computer use, pull request, schedule and other specialised tools are listed by name only, and the agent loads one when it needs it. Every request is smaller and cheaper; the first use of such a tool takes one more step. Applies to new sessions.') }}</span>
        <span
          v-if="deferForced !== null"
          class="set-desc"
        >{{ tt('Set by your administrator') }}</span>
      </div>
      <HToggle
        v-model="deferValue"
        :label="tt('Load rarely used tools on demand')"
        :disabled="!loaded || deferForced !== null"
      />
    </div>
  </section>
</template>
