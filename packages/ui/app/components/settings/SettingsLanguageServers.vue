<script setup lang="ts">
// Settings > Sessions > Language servers: the per-user switches of the LSP tool and of the errors reported after edits
// (UserPreferences, kept in the user's synced settings on the harness server). Saves at once; a failed save puts the
// toggle back.
type Keys = 'lspEnabled' | 'lspDiagnostics' | 'lspDiagnosticsStartServer'

const store = useSessionStore()

const state = ref<Record<Keys, boolean>>({ lspEnabled: true, lspDiagnostics: true, lspDiagnosticsStartServer: false })
const loaded = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function apply(p: Record<Keys, boolean>) {
  state.value = { lspEnabled: p.lspEnabled, lspDiagnostics: p.lspDiagnostics, lspDiagnosticsStartServer: p.lspDiagnosticsStartServer }
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

async function save(key: Keys, v: boolean) {
  if (!store.transport) return
  const prev = state.value[key]
  state.value[key] = v
  try {
    apply(await store.transport.updateUserPreferences({ [key]: v }))
    error.value = ''
  } catch (e) {
    state.value[key] = prev
    error.value = message(e)
  }
}

const rows: { key: Keys, label: string, desc: string }[] = [
  { key: 'lspEnabled', label: tt('Language server tool'), desc: tt('The agent can ask language servers installed on this machine for definitions, references and types. Off also stops the errors below.') },
  { key: 'lspDiagnostics', label: tt('Errors after edits'), desc: tt('After an edit, a language server that is already running tells the agent about new errors.') },
  { key: 'lspDiagnosticsStartServer', label: tt('Start a server for an edit'), desc: tt('Edits start the language server of the file if none runs yet, in trusted folders.') }
]
</script>

<template>
  <section
    :aria-label="tt('Language servers')"
    class="mt-6 flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Language servers') }}
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
    <div
      v-for="row in rows"
      :key="row.key"
      class="set-row"
    >
      <div class="set-label">
        <span>{{ row.label }}</span>
        <span class="set-desc">{{ row.desc }}</span>
      </div>
      <HToggle
        :model-value="state[row.key]"
        :label="row.label"
        :disabled="!loaded"
        @update:model-value="(v: boolean) => save(row.key, v)"
      />
    </div>
  </section>
</template>
