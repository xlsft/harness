<script setup lang="ts">
// Settings > Images (docs/design/images.md): the image model of the ImageGenerate tool. Each user picks one for all
// their devices (synced) or follows the server default; a device may use a model of a local OpenAI-compatible server
// instead (never synced, wins there). Below: the providers of Settings > Models that can make images, each with its logo
// (from the preset it was connected from), its image models with their price and, for admins, who may use each global
// model, and a form to add one. The server decides what is allowed; this screen shows its answers.
import type { ImageModelView, ImagePricingView, ImageProviderView, ImagesApi, ImageSettingsView } from '~/lib/images/types'
import { providerTile } from '~/lib/issues/types'
import { PROVIDER_LOGOS } from '~/utils/providerLogos'
import { imageApiLabel } from '~/lib/images/types'
import type { LocalProvider } from '~/lib/transport/localModels'

const props = defineProps<{ serverOnly?: boolean }>()
const store = useSessionStore()

const view = ref<ImageSettingsView | null>(null)
const mine = ref('')
const localProviders = ref<LocalProvider[]>([])
const localOff = ref(false)
const deviceProvider = ref('')
const deviceModel = ref('')
const error = ref('')
const busy = ref(false)
// Provider id → logo key in providerLogos, from the preset each Models provider was connected from.
const icons = ref<Record<string, string>>({})

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'

function api(): ImagesApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.images()
}
function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function run(f: () => Promise<void>) {
  busy.value = true
  error.value = ''
  try {
    await f()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function load() {
  await run(async () => {
    view.value = await api().settings()
    void loadIcons()
    if (props.serverOnly || !store.transport) return
    mine.value = (await store.transport.getUserPreferences()).imageModel
    await store.refreshDeviceSettings()
    const local = await store.transport.localModels().list().catch(() => ({ providers: [], disabledByPolicy: true }))
    localOff.value = local.disabledByPolicy
    // Ollama has no image API.
    localProviders.value = local.providers.filter(p => p.enabled && p.kind !== 'ollama')
    const m = /^local:([^/]+)\/(.+)$/.exec(store.deviceSettings.imageModel)
    deviceProvider.value = m?.[1] ?? ''
    deviceModel.value = m?.[2] ?? ''
  })
}
onMounted(load)

async function loadIcons() {
  try {
    const p = store.transport!.providers()
    const [list, presets] = await Promise.all([p.list(), p.presets()])
    const byPreset = Object.fromEntries(presets.presets.map(x => [x.id, x.icon]))
    icons.value = Object.fromEntries(list.providers.map(x => [x.id, byPreset[x.presetId] ?? '']))
  } catch {
    // Monograms of the names, then.
  }
}
const tileOf = (p: ImageProviderView) => providerTile(icons.value[p.id] ?? '', p.name, PROVIDER_LOGOS)
const modelsOf = (providerId: string) => (view.value?.models ?? []).filter(m => m.providerId === providerId)
// Models of a provider the list no longer has (removed in Settings > Models) still show, under their provider's name.
// One row per provider that can make images, then models whose provider is gone; each with its models under it.
const groups = computed(() => {
  const v = view.value
  if (!v) return []
  const out = v.providers.map((p) => {
    const models = modelsOf(p.id)
    const kind = p.api === 'gemini' ? tt('Gemini and Imagen image models') : p.api === 'openrouter' ? tt('Image models through OpenRouter') : tt('OpenAI-compatible image models')
    return { key: p.id, provider: p as ImageProviderView | null, name: p.name, models, about: models.length ? tt('{0} · {1} added', [kind, models.length]) : tt('{0} · none added yet', [kind]) }
  })
  if (orphans.value.length) out.push({ key: '', provider: null, name: orphans.value[0]!.providerName || tt('Removed provider'), models: orphans.value, about: tt('Its provider is no longer in Settings > Models') })
  return out
})
const orphans = computed(() => (view.value?.models ?? []).filter(m => !view.value?.providers.some(p => p.id === m.providerId)))

const usable = computed(() => (view.value?.models ?? []).filter(m => m.usable && m.enabled))
const nameOf = (id: string) => view.value?.models.find(m => m.id === id)?.name ?? id
const defaultName = computed(() => view.value?.defaultModel ? nameOf(view.value.defaultModel) : '')
const myOptions = computed(() => [
  { value: '', label: tt('Server default'), hint: defaultName.value || tt('none') },
  ...usable.value.map(m => ({ value: m.id, label: m.name, hint: m.providerName }))
])
const defaultOptions = computed(() => [
  { value: '', label: tt('None') },
  ...(view.value?.models ?? []).filter(m => !m.personal && m.enabled).map(m => ({ value: m.id, label: m.name, hint: m.providerName }))
])
const deviceProviderOptions = computed(() => [
  { value: '', label: tt('None') },
  ...localProviders.value.map(p => ({ value: p.id, label: p.name || p.id, hint: p.baseUrl }))
])

async function setMine(id: string) {
  await run(async () => {
    mine.value = (await store.transport!.updateUserPreferences({ imageModel: id })).imageModel
  })
}

async function setDevice(clear = false) {
  await run(async () => {
    const id = clear || !deviceProvider.value || !deviceModel.value.trim() ? '' : `local:${deviceProvider.value}/${deviceModel.value.trim()}`
    await store.updateDeviceSettings({ imageModel: id })
    if (clear) {
      deviceProvider.value = ''
      deviceModel.value = ''
    }
  })
}

async function setDefault(id: string) {
  await run(async () => {
    const d = await api().setDefault(id)
    if (view.value) view.value.defaultModel = d
  })
}

function priceText(p: ImagePricingView | null) {
  if (!p) return tt('No price: requests count no cost')
  const parts: string[] = []
  if (p.perImage) parts.push(tt('${0} per image', [p.perImage]))
  if (p.input || p.output) parts.push(tt('${0} in / ${1} out per 1M tokens', [p.input, p.output]))
  return parts.join(' + ') || tt('Free')
}
function rolesText(m: ImageModelView) {
  return m.roles.length ? tt('Only {0}', [m.roles.join(', ')]) : tt('Everyone')
}

async function patchModel(m: ImageModelView, patch: Parameters<ImagesApi['update']>[1]) {
  await run(async () => {
    const next = await api().update(m.id, patch)
    if (view.value) view.value.models = view.value.models.map(x => x.id === m.id ? next : x)
  })
}
function toggleRole(m: ImageModelView, role: string) {
  const roles = m.roles.includes(role) ? m.roles.filter(r => r !== role) : [...m.roles, role]
  return patchModel(m, { roles })
}
async function remove(m: ImageModelView) {
  await run(async () => {
    await api().remove(m.id)
    await load()
  })
}

// Editing a price: one row at a time.
const pricing = ref<{ id: string, perImage: string, input: string, output: string } | null>(null)
function editPrice(m: ImageModelView) {
  pricing.value = { id: m.id, perImage: String(m.pricing?.perImage ?? 0), input: String(m.pricing?.input ?? 0), output: String(m.pricing?.output ?? 0) }
}
async function savePrice(m: ImageModelView) {
  const p = pricing.value
  if (!p) return
  await patchModel(m, { pricing: { perImage: Number(p.perImage) || 0, input: Number(p.input) || 0, output: Number(p.output) || 0 } })
  if (!error.value) pricing.value = null
}

// Adding a model, under the provider whose "Add model" was pressed.
const draft = reactive({ providerId: '', upstreamModel: '', name: '', perImage: '', roles: [] as string[] })
const draftProvider = computed(() => view.value?.providers.find(p => p.id === draft.providerId))
function startAdd(p: ImageProviderView) {
  Object.assign(draft, { providerId: p.id, upstreamModel: '', name: '', perImage: '', roles: [] })
}
const placeholderOf = (p?: ImageProviderView) => p?.api === 'gemini' ? 'gemini-2.5-flash-image' : p?.api === 'openrouter' ? 'google/gemini-2.5-flash-image' : 'gpt-image-1'
async function add() {
  await run(async () => {
    const perImage = Number(draft.perImage)
    await api().add({
      providerId: draft.providerId,
      upstreamModel: draft.upstreamModel.trim(),
      name: draft.name.trim() || undefined,
      roles: draftProvider.value?.personal ? undefined : draft.roles,
      pricing: draft.perImage.trim() && Number.isFinite(perImage) ? { perImage, input: 0, output: 0 } : undefined
    })
    Object.assign(draft, { providerId: '', upstreamModel: '', name: '', perImage: '', roles: [] })
    view.value = await api().settings()
  })
}
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ tt('Images') }}
    </h2>
    <p class="set-sub">
      {{ tt('The model the agent uses to make images (the ImageGenerate tool). Without one, sessions have no such tool.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <template v-if="view">
      <div
        v-if="!serverOnly"
        class="set-row"
      >
        <div class="set-label min-w-0 flex-1">
          <span>{{ tt('Your image model') }}</span>
          <span class="set-desc">{{ tt('On all your devices. Server default follows what the administrator chose.') }}</span>
        </div>
        <div class="w-[300px] shrink-0 max-sm:w-full">
          <HSelect
            :model-value="mine"
            :options="myOptions"
            :aria-label="tt('Your image model')"
            :disabled="busy"
            @update:model-value="setMine(String($event ?? ''))"
          />
        </div>
      </div>

      <div
        v-if="!serverOnly && !localOff"
        class="set-row"
      >
        <div class="set-label min-w-0 flex-1">
          <span>{{ tt('On this device') }}</span>
          <span class="set-desc">{{ tt('A local OpenAI-compatible server with an images endpoint. Kept on this device only; it wins over your image model here.') }}</span>
        </div>
        <div class="flex w-[300px] shrink-0 flex-col gap-2 max-sm:w-full">
          <HSelect
            v-model="deviceProvider"
            :options="deviceProviderOptions"
            :aria-label="tt('Local provider')"
            :disabled="busy"
          />
          <input
            v-if="deviceProvider"
            v-model="deviceModel"
            :class="input"
            :placeholder="tt('Model name on that server')"
            :aria-label="tt('Local image model')"
          >
          <div class="flex gap-2">
            <HBtn
              size="sm"
              :disabled="busy || (!!deviceProvider && !deviceModel.trim())"
              @click="setDevice()"
            >
              {{ tt('Save') }}
            </HBtn>
            <HBtn
              v-if="store.deviceSettings.imageModel"
              size="sm"
              tone="muted"
              :disabled="busy"
              @click="setDevice(true)"
            >
              {{ tt('Clear') }}
            </HBtn>
          </div>
          <span
            v-if="store.deviceSettings.imageModel"
            class="set-desc"
          >{{ tt('In use here: {0}', [store.deviceSettings.imageModel]) }}</span>
        </div>
      </div>

      <div class="set-row">
        <div class="set-label min-w-0 flex-1">
          <span>{{ tt('Server default') }}</span>
          <span class="set-desc">{{ view.canManage ? tt('For everyone who has not picked a model and may use this one.') : tt('Chosen by the administrator.') }}</span>
        </div>
        <div
          v-if="view.canManage"
          class="w-[300px] shrink-0 max-sm:w-full"
        >
          <HSelect
            :model-value="view.defaultModel"
            :options="defaultOptions"
            :aria-label="tt('Server default image model')"
            :disabled="busy"
            @update:model-value="setDefault(String($event ?? ''))"
          />
        </div>
        <span
          v-else
          class="text-[12.5px]"
          :class="defaultName ? 'text-strong' : 'text-muted'"
        >{{ defaultName || tt('None') }}</span>
      </div>

      <h3 class="m-0 mt-6 text-[13px] font-semibold text-strong">
        {{ tt('Image models') }}
      </h3>
      <p class="m-0 mt-1 mb-3 text-[12px] text-muted">
        {{ tt('They run on the providers you connected in Settings > Models, with the same keys. Prices are approximate until you set them.') }}
      </p>
      <p
        v-if="!groups.length"
        class="m-0 text-[12px] text-muted"
      >
        {{ tt('Connect an OpenAI, Gemini, OpenRouter or other OpenAI-compatible provider in Settings > Models to add image models.') }}
      </p>
      <div
        v-else
        class="overflow-hidden border border-default"
      >
        <div
          v-for="(g, gi) in groups"
          :key="g.key"
          class="flex flex-col"
          :class="{ 'border-t border-default': gi > 0 }"
        >
          <div class="flex items-center gap-3 px-3 py-2.5">
            <SettingsMonogram
              :text="g.provider ? tileOf(g.provider) : 'PR'"
              :dim="!g.models.length"
            />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="flex items-center gap-2 text-[12.5px] text-strong">
                {{ g.name }}
                <span
                  v-if="g.provider?.personal"
                  class="text-[11px] text-muted"
                >{{ tt('yours') }}</span>
              </span>
              <span class="truncate text-[11.5px] text-muted">{{ g.about }}</span>
            </span>
            <HBtn
              v-if="g.provider && draft.providerId !== g.provider.id"
              size="sm"
              @click="startAdd(g.provider)"
            >
              <HIcon
                name="plus"
                :size="12"
              />{{ tt('Add model') }}
            </HBtn>
          </div>
          <form
            v-if="g.provider && draft.providerId === g.provider.id"
            class="flex flex-col gap-2 border-t border-default py-3 pr-3 pl-[52px] max-sm:pl-3"
            :aria-label="tt('Add an image model')"
            @submit.prevent="add"
          >
            <div class="grid gap-2 sm:grid-cols-2">
              <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Model id at the provider') }}
                <input
                  v-model="draft.upstreamModel"
                  :class="input"
                  :placeholder="placeholderOf(draftProvider)"
                  required
                >
              </label>
              <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Name') }}
                <input
                  v-model="draft.name"
                  :class="input"
                  :placeholder="tt('Shown in pickers (optional)')"
                >
              </label>
              <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Price per image, USD') }}
                <input
                  v-model="draft.perImage"
                  :class="input"
                  inputmode="decimal"
                  :placeholder="tt('Approximate default when empty')"
                >
              </label>
            </div>
            <div
              v-if="draftProvider && !draftProvider.personal"
              class="flex flex-wrap items-center gap-1.5"
            >
              <span class="text-[11px] text-muted">{{ tt('Only for (none: everyone):') }}</span>
              <HBtn
                v-for="r in view.roles"
                :key="r"
                size="xs"
                :variant="draft.roles.includes(r) ? 'primary' : 'outline'"
                :aria-pressed="draft.roles.includes(r)"
                @click="draft.roles = draft.roles.includes(r) ? draft.roles.filter(x => x !== r) : [...draft.roles, r]"
              >
                {{ r }}
              </HBtn>
            </div>
            <div class="flex justify-end gap-2">
              <HBtn
                size="md"
                @click="draft.providerId = ''"
              >
                {{ tt('Cancel') }}
              </HBtn>
              <HBtn
                size="md"
                variant="primary"
                type="submit"
                :disabled="busy || !draft.providerId || !draft.upstreamModel.trim()"
              >
                {{ tt('Add model') }}
              </HBtn>
            </div>
          </form>
          <div
            v-for="m in g.models"
            :key="m.id"
            class="flex items-start gap-3 border-t border-default py-2.5 pr-3 pl-[52px] max-sm:flex-col max-sm:pl-3"
          >
            <div class="set-label min-w-0 flex-1">
              <span class="flex items-center gap-2">
                <HIcon
                  name="image"
                  :size="13"
                />
                {{ m.name }}
                <span
                  v-if="!m.usable && m.enabled"
                  class="text-[11px] text-muted"
                >{{ tt('(not for your role)') }}</span>
              </span>
              <span class="set-desc">{{ m.providerName }} · {{ m.upstreamModel }} · {{ imageApiLabel(m.api) }}{{ m.edits ? '' : ` · ${tt('text only')}` }}</span>
              <span class="set-desc">{{ priceText(m.pricing) }}<template v-if="!m.personal"> · {{ rolesText(m) }}</template></span>
              <div
                v-if="pricing?.id === m.id"
                class="mt-2 grid grid-cols-3 gap-2"
              >
                <label class="flex flex-col gap-1 text-[11px] text-muted">{{ tt('Per image, USD') }}<input
                  v-model="pricing.perImage"
                  :class="input"
                  inputmode="decimal"
                ></label>
                <label class="flex flex-col gap-1 text-[11px] text-muted">{{ tt('Input per 1M tokens') }}<input
                  v-model="pricing.input"
                  :class="input"
                  inputmode="decimal"
                ></label>
                <label class="flex flex-col gap-1 text-[11px] text-muted">{{ tt('Output per 1M tokens') }}<input
                  v-model="pricing.output"
                  :class="input"
                  inputmode="decimal"
                ></label>
                <div class="col-span-3 flex gap-2">
                  <HBtn
                    size="sm"
                    variant="primary"
                    :disabled="busy"
                    @click="savePrice(m)"
                  >
                    {{ tt('Save price') }}
                  </HBtn>
                  <HBtn
                    size="sm"
                    tone="muted"
                    @click="pricing = null"
                  >
                    {{ tt('Cancel') }}
                  </HBtn>
                </div>
              </div>
              <div
                v-if="m.editable && view.canManage && !m.personal"
                class="mt-2 flex flex-wrap items-center gap-1.5"
                role="group"
                :aria-label="tt('Who may use it')"
              >
                <span class="text-[11px] text-muted">{{ tt('Who may use it:') }}</span>
                <HBtn
                  v-for="r in view.roles"
                  :key="r"
                  size="xs"
                  :variant="m.roles.includes(r) ? 'primary' : 'outline'"
                  :aria-pressed="m.roles.includes(r)"
                  :disabled="busy"
                  @click="toggleRole(m, r)"
                >
                  {{ r }}
                </HBtn>
              </div>
            </div>
            <div
              v-if="m.editable"
              class="flex shrink-0 items-center gap-2"
            >
              <HBtn
                size="sm"
                tone="muted"
                :disabled="busy"
                @click="editPrice(m)"
              >
                {{ tt('Price') }}
              </HBtn>
              <HToggle
                :model-value="m.enabled"
                :label="tt('Enabled')"
                :disabled="busy"
                @update:model-value="patchModel(m, { enabled: $event })"
              />
              <HIconBtn
                icon="x"
                :label="tt('Remove {0}', [m.name])"
                :size="26"
                @click="remove(m)"
              />
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
