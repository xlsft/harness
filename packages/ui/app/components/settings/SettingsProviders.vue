<script setup lang="ts">
// Settings > Providers: where models and auto-mode decisions come from, in two sources, "LLM models" and
// "Classifiers". Each lists the presets the server offers (connect with just a key) and then custom
// providers. A connection is the user's own, or, for admins who choose so, everyone's; classifier
// providers and the classifier settings are server-wide (admins). Keys are write-only. Changes refresh the
// model catalog, so the Models section follows.
import type { PresetList, ProviderList, ProvidersApi } from '~/lib/providers/types'

const emit = defineEmits<{ changed: [list: ProviderList] }>()
const store = useSessionStore()

const list = ref<ProviderList>({ providers: [], types: [], canManageGlobal: false })
const presets = ref<PresetList>({ presets: [], protocols: [] })
const loading = ref(true)
const error = ref('')

function api(): ProvidersApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.providers()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load() {
  try {
    const [l, p] = await Promise.all([api().list(), presets.value.presets.length ? Promise.resolve(presets.value) : api().presets()])
    list.value = l
    presets.value = p
    error.value = ''
    emit('changed', l)
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// Reloads the providers and the model catalog after a change.
async function changed() {
  error.value = ''
  await load()
  try {
    if (store.transport) store.models = await store.transport.listModels()
  } catch {
    // The catalog refreshes on the next start.
  }
}

const groups = computed(() => [
  {
    key: 'llm' as const,
    title: 'LLM models',
    sub: 'Services whose models you chat with. Connect one with its API key, then switch on the models you want in the picker.'
  },
  {
    key: 'classifier' as const,
    title: 'Classifiers',
    sub: 'Services auto mode asks whether a tool call is safe to run. Server-wide, so admins connect them.'
  }
].map(g => ({
  ...g,
  presets: presets.value.presets.filter(p => p.group === g.key),
  protocols: presets.value.protocols.filter(p => p.group === g.key),
  providers: list.value.providers.filter(p => p.group === g.key)
})))
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      Providers
    </h2>
    <p class="set-sub">
      Where models and auto-mode decisions come from. Keys are stored encrypted on the server and never shown again; admins can see that a key is set but cannot read or use yours.
    </p>

    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 flex items-start gap-2 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      <span class="flex-1">{{ error }}</span>
      <HIconBtn
        icon="x"
        label="Dismiss"
        :size="26"
        :icon-size="12"
        @click="error = ''"
      />
    </p>
    <p
      v-if="loading"
      class="m-0 py-4 text-[12.5px] text-muted"
    >
      Loading…
    </p>

    <template v-else>
      <SettingsProviderGroup
        v-for="g in groups"
        :key="g.key"
        :group="g.key"
        :title="g.title"
        :sub="g.sub"
        :presets="g.presets"
        :protocols="g.protocols"
        :providers="g.providers"
        :can-manage-global="list.canManageGlobal"
        :api="api()"
        @changed="changed"
        @error="error = $event"
      >
        <SettingsClassifier
          v-if="g.key === 'classifier' && list.canManageGlobal"
          :providers="list.providers"
          :presets="presets.presets"
        />
      </SettingsProviderGroup>
    </template>
  </div>
</template>
