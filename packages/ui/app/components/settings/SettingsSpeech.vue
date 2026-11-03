<script setup lang="ts">
// Settings > Speech: dictation (speech to text) and "Read aloud" (text to speech), one block each
// (SettingsSpeechKind). A block picks the provider for all devices (the server's providers, synced) and for this
// device (an engine on this computer or the browser's own, never synced; it wins here), then its model, language
// or voice, and lists the providers: local engines with their downloadable models, the server's with their key
// (admins set it). Every change saves at once.
import type { LocalSpeechModelView, SpeechApi, SpeechProvidersList, SpeechSettings, SpeechSettingsPatch } from '~/lib/speech/types'

const store = useSessionStore()

const list = ref<SpeechProvidersList>({ providers: [], serverError: '', canManageGlobal: false })
const settings = ref<SpeechSettings | null>(null)
const models = ref<LocalSpeechModelView[]>([])
const loading = ref(true)
const error = ref('')

function api(): SpeechApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.speech()
}
function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load() {
  try {
    const [l, s, m] = await Promise.all([api().listProviders(), api().getSettings(), api().listLocalModels()])
    list.value = l
    settings.value = s
    models.value = m.models
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function update(patch: SpeechSettingsPatch) {
  error.value = ''
  try {
    settings.value = await api().updateSettings(patch)
  } catch (e) {
    error.value = message(e)
  }
}
</script>

<template>
  <div class="flex flex-col">
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
    <p
      v-else-if="list.serverError"
      class="m-0 mb-3 text-[12px] text-muted"
    >
      The server's providers are unavailable: {{ list.serverError }}
    </p>

    <template v-if="!loading && settings">
      <SettingsSpeechKind
        v-for="kind in (['stt', 'tts'] as const)"
        :key="kind"
        :kind="kind"
        :providers="list.providers.filter(p => p.kind === kind)"
        :settings="settings"
        :models="models"
        :can-manage-global="list.canManageGlobal"
        :api="api()"
        @update="update"
        @reload="load"
        @error="error = $event"
      />
    </template>
  </div>
</template>
