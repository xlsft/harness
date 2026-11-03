<script setup lang="ts">
// One block of Settings > Speech: dictation (stt) or Read aloud (tts). The choice for all devices (the server's
// providers, synced) and for this device (an engine on this computer or the browser's own; it wins here), the
// model and language or voice and speed of the provider in effect, then the providers: engines on this computer
// with their models to download, the server's with their key (admins).
import type { HSelectOption } from '~/components/HSelect.vue'
import { BROWSER_SPEECH_PROVIDER, type LocalSpeechModelView, type SpeechApi, type SpeechKindName, type SpeechProviderView, type SpeechSettings, type SpeechSettingsPatch, type SttChoice, type TtsChoice } from '~/lib/speech/types'

const props = defineProps<{
  kind: SpeechKindName
  providers: SpeechProviderView[]
  settings: SpeechSettings
  models: LocalSpeechModelView[]
  canManageGlobal: boolean
  api: SpeechApi
}>()
const emit = defineEmits<{ update: [patch: SpeechSettingsPatch], reload: [], error: [message: string] }>()

const stt = computed(() => props.kind === 'stt')
const local = computed(() => props.providers.filter(p => p.location === 'local'))
const global = computed(() => props.providers.filter(p => p.location === 'global'))
// The browser's own speech: synthesis everywhere, recognition only where the WebView has it (not WebKit).
const browserOk = computed(() => import.meta.client && (stt.value
  ? 'SpeechRecognition' in window || 'webkitSpeechRecognition' in window
  : 'speechSynthesis' in window))

const synced = computed(() => (stt.value ? props.settings.synced.stt : props.settings.synced.tts) as SttChoice & TtsChoice)
const device = computed(() => (stt.value ? props.settings.device.stt : props.settings.device.tts) as SttChoice & TtsChoice)
const effective = computed(() => (stt.value ? props.settings.stt : props.settings.tts) as SttChoice & TtsChoice)
const provider = computed(() => props.providers.find(p => p.id === effective.value.providerId))
// Edits of the model, language, voice and speed go to the part that is in effect here.
const owner = computed<'device' | 'synced'>(() => device.value.providerId ? 'device' : 'synced')

function patch(part: 'device' | 'synced', choice: Partial<SttChoice & TtsChoice>) {
  const base = part === 'device' ? device.value : synced.value
  const next = { ...base, ...choice }
  const value = stt.value
    ? { providerId: next.providerId, model: next.model, language: next.language ?? '' }
    : { providerId: next.providerId, model: next.model, voice: next.voice ?? '', speed: next.speed ?? 1 }
  const key = `${part}${stt.value ? 'Stt' : 'Tts'}` as keyof SpeechSettingsPatch
  emit('update', { [key]: value } as SpeechSettingsPatch)
}
// A new provider starts from its defaults.
const pick = (part: 'device' | 'synced', providerId: string) => patch(part, { providerId, model: '', voice: '' })

const status = (p: SpeechProviderView) => p.available ? '' : p.unavailableReason || (p.location === 'global' ? 'not set up' : 'not installed')
const deviceOptions = computed<HSelectOption[]>(() => [
  { value: '', label: 'Same as all devices' },
  ...(browserOk.value ? [{ value: BROWSER_SPEECH_PROVIDER, label: stt.value ? 'Browser dictation' : 'Browser voice', hint: 'built in' }] : []),
  ...local.value.map(p => ({ value: p.id, label: p.name, hint: status(p) || 'on this computer' }))
])
const syncedOptions = computed<HSelectOption[]>(() => [
  { value: '', label: 'None' },
  ...global.value.map(p => ({ value: p.id, label: p.name, hint: status(p) }))
])
const modelOptions = computed<HSelectOption[]>(() => [
  { value: '', label: 'Default', hint: provider.value?.defaultModel },
  ...(provider.value?.models ?? []).map(m => ({ value: m.id, label: m.name, hint: provider.value?.location === 'local' && !m.installed ? 'not downloaded' : '' }))
])

const LANGUAGES: HSelectOption[] = [
  { value: '', label: 'Detect' },
  ...[['en', 'English'], ['ru', 'Russian'], ['uk', 'Ukrainian'], ['de', 'German'], ['fr', 'French'], ['es', 'Spanish'], ['it', 'Italian'], ['pt', 'Portuguese'], ['pl', 'Polish'], ['tr', 'Turkish'], ['zh', 'Chinese'], ['ja', 'Japanese'], ['ko', 'Korean']]
    .map(([value, label]) => ({ value: value!, label: label! }))
]
const SPEEDS: HSelectOption[] = ['0.75', '1', '1.25', '1.5', '2'].map(v => ({ value: v, label: `${v}×` }))

// Voices: the provider's own list when it has one (fetched), else the ones it came with.
const voices = ref<HSelectOption[]>([])
watch(() => [provider.value?.id, effective.value.model], async () => {
  const p = provider.value
  if (stt.value || !p) return (voices.value = [])
  const toOption = (v: { id: string, name: string, language: string, installed: boolean }) =>
    ({ value: v.id, label: v.name, hint: [v.language, p.location === 'local' && !v.installed ? 'not downloaded' : ''].filter(Boolean).join(' · ') })
  voices.value = [{ value: '', label: 'Default', hint: p.defaultVoice }, ...p.voices.map(toOption)]
  if (!p.listsVoices) return
  try {
    voices.value = [{ value: '', label: 'Default', hint: p.defaultVoice }, ...(await props.api.listVoices(p.id, effective.value.model || undefined)).map(toOption)]
  } catch { /* the built-in list stays */ }
}, { immediate: true })

// Preview: a sentence in the voice in effect.
const previewing = ref(false)
let player: HTMLAudioElement | null = null
async function preview() {
  if (previewing.value) {
    player?.pause()
    previewing.value = false
    return
  }
  previewing.value = true
  try {
    if (effective.value.providerId === BROWSER_SPEECH_PROVIDER) {
      useReadAloud().toggle('speech-preview', 'This is how answers will sound when read aloud.')
      previewing.value = false
      return
    }
    const { audio } = await props.api.synthesize('This is how answers will sound when read aloud.', { plainText: true }).done
    player = new Audio(URL.createObjectURL(audio))
    player.onended = player.onerror = () => (previewing.value = false)
    await player.play()
  } catch (e) {
    previewing.value = false
    emit('error', e instanceof Error ? e.message : String(e))
  }
}
onBeforeUnmount(() => player?.pause())

// Local models: download with progress, delete.
const progress = reactive<Record<string, number>>({})
const modelsOf = (p: SpeechProviderView) => props.models.filter(m => m.providerId === p.id && m.kind === props.kind)
async function download(m: LocalSpeechModelView) {
  progress[m.id] = 0
  try {
    await props.api.downloadModel(m.id, (got, total) => (progress[m.id] = total ? got / total : 0)).done
    emit('reload')
  } catch (e) {
    emit('error', e instanceof Error ? e.message : String(e))
  } finally {
    Reflect.deleteProperty(progress, m.id)
  }
}
async function remove(m: LocalSpeechModelView) {
  try {
    await props.api.deleteModel(m.id)
    emit('reload')
  } catch (e) {
    emit('error', e instanceof Error ? e.message : String(e))
  }
}
const mb = (n: number) => n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : `${Math.max(1, Math.round(n / 1e6))} MB`

// Global providers: the admins' key.
const keying = ref('')
const keyValue = ref('')
const saving = ref(false)
async function saveKey(p: SpeechProviderView) {
  saving.value = true
  try {
    await props.api.setProviderKey(p.id, keyValue.value.trim())
    keying.value = ''
    keyValue.value = ''
    emit('reload')
  } catch (e) {
    emit('error', e instanceof Error ? e.message : String(e))
  } finally {
    saving.value = false
  }
}
async function clearKey(p: SpeechProviderView) {
  try {
    await props.api.clearProviderKey(p.id)
    emit('reload')
  } catch (e) {
    emit('error', e instanceof Error ? e.message : String(e))
  }
}
</script>

<template>
  <section
    class="mb-8 flex flex-col"
    :aria-label="stt ? 'Speech to text' : 'Text to speech'"
  >
    <h3 class="m-0 mb-1 text-[13px] font-semibold text-strong">
      {{ stt ? 'Speech to text' : 'Text to speech' }}
    </h3>
    <p class="m-0 mb-1 text-[12px] text-muted">
      {{ stt ? 'Turns what you say into a message in the composer.' : 'Reads answers aloud from the speaker button under them.' }}
    </p>

    <div class="set-row">
      <div class="set-label">
        <span>All devices</span>
        <span class="set-desc">{{ settings.syncedAvailable ? 'One of the server\'s providers, the same on every device.' : 'Needs a connection to the server.' }}</span>
      </div>
      <div class="w-[240px] shrink-0 max-sm:w-full">
        <HSelect
          :model-value="synced.providerId"
          :options="syncedOptions"
          :disabled="!settings.syncedAvailable"
          aria-label="Provider for all devices"
          @update:model-value="pick('synced', $event)"
        />
      </div>
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>This device</span>
        <span class="set-desc">Runs on this computer and wins over the choice for all devices here. Never synced.</span>
      </div>
      <div class="w-[240px] shrink-0 max-sm:w-full">
        <HSelect
          :model-value="device.providerId"
          :options="deviceOptions"
          aria-label="Provider on this device"
          @update:model-value="pick('device', $event)"
        />
      </div>
    </div>

    <template v-if="provider">
      <div
        v-if="provider.models.length"
        class="set-row"
      >
        <div class="set-label">
          <span>Model</span>
          <span class="set-desc">Of {{ provider.name }}.</span>
        </div>
        <div class="w-[240px] shrink-0 max-sm:w-full">
          <HSelect
            :model-value="effective.model"
            :options="modelOptions"
            aria-label="Model"
            @update:model-value="patch(owner, { model: $event })"
          />
        </div>
      </div>
      <div
        v-if="stt && provider.supportsLanguage"
        class="set-row"
      >
        <div class="set-label">
          <span>Language</span>
          <span class="set-desc">A hint for recognition; Detect works it out from the audio.</span>
        </div>
        <div class="w-[240px] shrink-0 max-sm:w-full">
          <HSelect
            :model-value="effective.language"
            :options="LANGUAGES"
            aria-label="Language"
            @update:model-value="patch(owner, { language: $event })"
          />
        </div>
      </div>
      <template v-if="!stt">
        <div
          v-if="voices.length > 1"
          class="set-row"
        >
          <div class="set-label">
            <span>Voice</span>
          </div>
          <div class="w-[240px] shrink-0 max-sm:w-full">
            <HSelect
              :model-value="effective.voice"
              :options="voices"
              aria-label="Voice"
              @update:model-value="patch(owner, { voice: $event })"
            />
          </div>
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Speed</span>
          </div>
          <div class="flex w-[240px] shrink-0 items-center gap-2 max-sm:w-full">
            <div class="min-w-0 flex-1">
              <HSelect
                :model-value="String(effective.speed || 1)"
                :options="SPEEDS"
                :disabled="!provider.supportsSpeed"
                aria-label="Speed"
                @update:model-value="patch(owner, { speed: Number($event) })"
              />
            </div>
            <HBtn
              size="md"
              :disabled="!provider.available"
              @click="preview"
            >
              <HIcon
                :name="previewing ? 'stop' : 'speaker'"
                :size="13"
              />{{ previewing ? 'Stop' : 'Preview' }}
            </HBtn>
          </div>
        </div>
      </template>
    </template>

    <div class="mt-3 overflow-hidden border border-default">
      <div
        v-for="(p, i) in [...local, ...global]"
        :key="p.id"
        class="flex flex-col"
        :class="i && 'border-t border-default'"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            :text="p.icon"
            :dim="!p.available"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ p.name }}
              <span class="text-[11px] text-muted">{{ p.location === 'local' ? 'this computer' : 'server' }}</span>
              <span
                class="text-[11px]"
                :class="p.available ? 'text-ok' : 'text-muted'"
              >{{ p.available ? 'ready' : p.location === 'global' ? 'not set up' : 'not installed' }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ p.description }}</span>
            <span
              v-if="!p.available && p.unavailableReason && !(p.location === 'global' && !p.keySet && canManageGlobal)"
              class="text-[11.5px] text-muted"
            >{{ p.unavailableReason }}</span>
          </span>
          <SettingsLink
            v-if="p.docsUrl"
            :href="p.docsUrl"
            class="max-sm:hidden"
          >
            Docs
          </SettingsLink>
          <template v-if="p.location === 'global' && canManageGlobal">
            <HBtn
              v-if="p.keySet"
              size="sm"
              tone="danger"
              @click="clearKey(p)"
            >
              Remove key
            </HBtn>
            <HBtn
              v-else-if="keying !== p.id"
              size="sm"
              @click="keying = p.id; keyValue = ''"
            >
              Set key
            </HBtn>
          </template>
        </div>

        <form
          v-if="keying === p.id"
          class="flex items-center gap-2 border-t border-default px-3 py-2.5"
          @submit.prevent="saveKey(p)"
        >
          <input
            v-model="keyValue"
            type="password"
            autocomplete="off"
            :placeholder="`${p.name} API key`"
            :aria-label="`${p.name} API key`"
            class="h-8 min-w-0 flex-1 border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent"
          >
          <HBtn
            size="md"
            @click="keying = ''"
          >
            Cancel
          </HBtn>
          <HBtn
            size="md"
            variant="primary"
            type="submit"
            :disabled="saving || !keyValue.trim()"
          >
            Save
          </HBtn>
        </form>
        <p
          v-if="p.location === 'global' && p.keySet && p.keyHint"
          class="m-0 px-3 pb-2.5 pl-[52px] text-[11.5px] text-muted"
        >
          Key {{ p.keyHint }}
        </p>

        <ul
          v-if="p.location === 'local' && modelsOf(p).length"
          class="m-0 flex list-none flex-col border-t border-default p-0"
        >
          <li
            v-for="m in modelsOf(p)"
            :key="m.id"
            class="flex items-center gap-3 py-2 pr-3 pl-[52px] text-[12px]"
          >
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="flex items-center gap-2 text-strong">
                {{ m.name }}
                <span
                  v-if="m.recommended"
                  class="text-[11px] text-accent"
                >recommended</span>
              </span>
              <span class="truncate text-[11.5px] text-muted">{{ [mb(m.sizeBytes), m.languages.slice(0, 4).join(', '), m.description].filter(Boolean).join(' · ') }}</span>
            </span>
            <span
              v-if="progress[m.id] !== undefined || m.downloading"
              class="flex w-[110px] shrink-0 items-center gap-2 text-[11px] text-muted tabular-nums"
            >
              <span class="h-1 flex-1 bg-raised">
                <span
                  class="block h-full bg-accent"
                  :style="{ width: `${Math.round((progress[m.id] ?? 0) * 100)}%` }"
                />
              </span>{{ Math.round((progress[m.id] ?? 0) * 100) }}%
            </span>
            <HBtn
              v-else-if="m.installed"
              size="sm"
              tone="danger"
              @click="remove(m)"
            >
              Delete
            </HBtn>
            <HBtn
              v-else
              size="sm"
              :disabled="m.unsupported"
              :title="m.unsupported ? 'Not available for this computer' : undefined"
              @click="download(m)"
            >
              Download
            </HBtn>
          </li>
        </ul>
      </div>
      <p
        v-if="!local.length && !global.length"
        class="m-0 px-3 py-3 text-[12px] text-muted"
      >
        No providers.
      </p>
    </div>
  </section>
</template>
