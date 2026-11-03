<script setup lang="ts">
// Settings > Models or Settings > Classifiers (`group`): where models, or auto-mode decisions, come from.
// Lists the presets the server offers (connect with just a key) and then custom providers; on Models each
// enabled model carries its default effort, under a Defaults block (the model per role). A connection is the user's own, or, for admins who choose so, everyone's; classifier
// providers are server-wide (admins), and so is the default classifier, which each user may override for
// themselves. Keys are write-only. Changes refresh the model catalog, so the Models section follows.
import type { PresetList, ProviderGroupName, ProviderList, ProvidersApi } from '~/lib/providers/types'

const props = defineProps<{ group: Exclude<ProviderGroupName, 'search'> }>()
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
    await classifier?.load(l.canManageGlobal)
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

const PAGES = {
  llm: {
    title: 'Models',
    sub: 'Services whose models you chat with. Connect one with its API key, switch on the models you want, and set each model\'s default effort. Keys are stored encrypted and never shown again; admins can see that a key is set but cannot read or use yours.'
  },
  classifier: {
    title: 'Classifiers',
    sub: 'Services auto mode asks whether a tool call is safe to run. Admins connect them and choose the default for everyone; you can pick another one for yourself.'
  }
} as const

// The classifier in use and its fallback model: the server default (admins) and the caller's own choice.
const classifier = props.group === 'classifier'
  ? createClassifierState((msg) => {
      error.value = msg
    })
  : null
if (classifier) provide(CLASSIFIER, classifier)

const page = computed(() => ({
  ...PAGES[props.group],
  presets: presets.value.presets.filter(p => p.group === props.group),
  protocols: presets.value.protocols.filter(p => p.group === props.group),
  providers: list.value.providers.filter(p => p.group === props.group)
}))
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ page.title }}
    </h2>
    <p class="set-sub">
      {{ page.sub }}
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
      <SettingsSlots
        v-if="group === 'llm'"
        :providers="list.providers"
        :api="api()"
      />
      <SettingsProviderGroup
        :group="group"
        title=""
        sub=""
        :presets="page.presets"
        :protocols="page.protocols"
        :providers="page.providers"
        :can-manage-global="list.canManageGlobal"
        :api="api()"
        @changed="changed"
        @error="error = $event"
      >
        <!-- Classifiers: the chat model that decides when no classifier is in use or it is unsure. -->
        <template
          v-if="group === 'classifier'"
          #rows
        >
          <SettingsModelClassifier
            :providers="list.providers"
            :can-manage-global="list.canManageGlobal"
          />
        </template>
      </SettingsProviderGroup>
    </template>
  </div>
</template>
