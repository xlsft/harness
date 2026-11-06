<script setup lang="ts">
// Settings > Sessions > Editor: how the agent's edits go through an attached editor (packages/vscode; UserPreferences, kept
// in the user's synced settings on the harness server). Saves at once; a failed save puts the toggle back.
type Keys = 'ideDiffs' | 'ideReviewAllEdits'

const store = useSessionStore()

const state = ref<Record<Keys, boolean>>({ ideDiffs: true, ideReviewAllEdits: false })
const loaded = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function apply(p: Record<Keys, boolean>) {
  state.value = { ideDiffs: p.ideDiffs, ideReviewAllEdits: p.ideReviewAllEdits }
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
  { key: 'ideDiffs', label: tt('Prompts open a diff'), desc: tt('When an edit would ask for permission, its change also opens in your editor\'s diff, where you can accept or reject it.') },
  { key: 'ideReviewAllEdits', label: tt('Review every edit first'), desc: tt('Edits the permission mode would allow wait for an answer in your editor\'s diff too. A rule that denies still denies.') }
]
</script>

<template>
  <section
    :aria-label="tt('Editor')"
    class="mt-6 flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Editor') }}
    </h2>
    <p class="set-sub">
      {{ tt('Applies while an editor extension is attached to the session. Saved to your account on the server, so it applies on every machine.') }}
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
