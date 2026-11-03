<script setup lang="ts">
// Settings > Search: where the WebSearch tool's results come from. The built-in SearXNG first (nothing to
// connect), then the search APIs the server offers as presets (connect with a key), then custom
// SearXNG-compatible instances. A connection is the user's own or, for admins who choose so, everyone's.
// Admins pick the default for everyone; each user may pick another provider for themselves. Keys are
// write-only; every change saves at once.
import type { PresetList, PresetView, ProviderDraft, ProviderList, ProviderView, ProvidersApi } from '~/lib/providers/types'
import { BUILTIN_SEARCH_ID } from '~/lib/providers/types'

const store = useSessionStore()

const list = ref<ProviderList>({ providers: [], types: [], canManageGlobal: false })
const presetList = ref<PresetList>({ presets: [], protocols: [] })
const loading = ref(true)
const error = ref('')

function api(): ProvidersApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.providers()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

const search = createSearchState(api, (msg) => {
  error.value = msg
})
provide(SEARCH, search)

async function load() {
  try {
    const [l, p] = await Promise.all([api().list(), presetList.value.presets.length ? Promise.resolve(presetList.value) : api().presets(), search.load()])
    list.value = l
    presetList.value = p
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// After a change: the providers, and the choice (a removed or disabled provider falls back to the default).
async function changed() {
  error.value = ''
  await load()
}

const presets = computed(() => presetList.value.presets.filter(p => p.group === 'search'))
const builtin = computed(() => presets.value.find(p => p.builtin))
const connectable = computed(() => presets.value.filter(p => !p.builtin))
const providers = computed(() => list.value.providers.filter(p => p.group === 'search'))
const presetIds = computed(() => new Set(connectable.value.map(p => p.id)))
const connectionsOf = (id: string) => providers.value.filter(p => p.presetId === id)
const custom = computed(() => providers.value.filter(p => !p.presetId || !presetIds.value.has(p.presetId)))
// A custom provider speaks SearXNG: it takes the SearXNG preset's options.
const searxngOptions = computed(() => presets.value.find(p => p.id === 'searxng-url')?.options ?? [])
const specsOf = (p: ProviderView, preset?: PresetView) => preset?.options ?? (p.type === 'searxng' ? searxngOptions.value : [])

// One connection per scope: one of the user's own, and one for everyone (admins).
function canConnect(p: PresetView) {
  return connectionsOf(p.id).filter(c => c.editable).length < (list.value.canManageGlobal ? 2 : 1)
}

const connecting = ref('')
const addingCustom = ref(false)
const busy = ref(false)
const fresh = ref('')

async function create(draft: ProviderDraft) {
  busy.value = true
  try {
    const p = await api().create(draft)
    fresh.value = p.id
    connecting.value = ''
    addingCustom.value = false
    await changed()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

// The built-in's own Test.
const builtinTest = ref<{ ok: boolean, message: string } | null>(null)
const testingBuiltin = ref(false)
async function testBuiltin() {
  testingBuiltin.value = true
  try {
    builtinTest.value = await api().test(BUILTIN_SEARCH_ID)
  } catch (e) {
    error.value = message(e)
  } finally {
    testingBuiltin.value = false
  }
}
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      Search
    </h2>
    <p class="set-sub">
      Where web search results come from. The built-in SearXNG works without a key; connect a search API for better results. Admins choose the default for everyone; you can pick another one for yourself. Keys are stored encrypted and never shown again.
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

    <section
      v-else
      aria-label="Search providers"
      class="mb-8 overflow-hidden border border-default"
    >
      <!-- Built in -->
      <div
        v-if="builtin"
        class="flex flex-col"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram :text="builtin.icon" />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ builtin.name }}
              <span class="text-[11px] text-ok">ready</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ builtin.description }}</span>
            <span
              v-if="builtinTest"
              role="status"
              class="text-[11.5px]"
              :class="builtinTest.ok ? 'text-ok' : 'text-bad'"
            >{{ builtinTest.message }}</span>
          </span>
          <SettingsLink
            v-if="builtin.docsUrl"
            :href="builtin.docsUrl"
            class="max-sm:hidden"
          >
            Docs
          </SettingsLink>
          <HBtn
            size="sm"
            :disabled="testingBuiltin"
            @click="testBuiltin"
          >
            {{ testingBuiltin ? 'Testing…' : 'Test' }}
          </HBtn>
        </div>
        <SettingsSearchChoice
          :provider-id="builtin.id"
          :name="builtin.name"
          shared
          enabled
          builtin
        />
      </div>

      <!-- Presets -->
      <div
        v-for="preset in connectable"
        :key="preset.id"
        class="flex flex-col border-t border-default"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            :text="preset.icon"
            :dim="!connectionsOf(preset.id).length"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ preset.name }}
              <span
                v-if="connectionsOf(preset.id).length"
                class="text-[11px] text-ok"
              >connected</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ preset.description }}</span>
          </span>
          <SettingsLink
            v-if="preset.docsUrl"
            :href="preset.docsUrl"
            class="max-sm:hidden"
          >
            Docs
          </SettingsLink>
          <HBtn
            v-if="canConnect(preset) && connecting !== preset.id"
            size="sm"
            @click="connecting = preset.id"
          >
            Connect
          </HBtn>
        </div>
        <SettingsSearchConnect
          v-if="connecting === preset.id"
          :preset="preset"
          :can-manage-global="list.canManageGlobal"
          :busy="busy"
          @submit="create"
          @cancel="connecting = ''"
        />
        <SettingsSearchCard
          v-for="c in connectionsOf(preset.id)"
          :key="c.id"
          :provider="c"
          :preset="preset"
          :specs="specsOf(c, preset)"
          :api="api()"
          :auto-open="fresh === c.id"
          @changed="changed"
          @error="error = $event"
        />
      </div>

      <!-- Custom -->
      <div class="flex flex-col border-t border-default">
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            icon="plus"
            :dim="!custom.length"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="text-[12.5px] text-strong">Custom</span>
            <span class="truncate text-[11.5px] text-muted">Any SearXNG-compatible instance: you set the name and URL.</span>
          </span>
          <HBtn
            v-if="!addingCustom"
            size="sm"
            @click="addingCustom = true"
          >
            <HIcon
              name="plus"
              :size="12"
            />Add custom
          </HBtn>
        </div>
        <SettingsSearchConnect
          v-if="addingCustom"
          :can-manage-global="list.canManageGlobal"
          :busy="busy"
          @submit="create"
          @cancel="addingCustom = false"
        />
        <SettingsSearchCard
          v-for="c in custom"
          :key="c.id"
          :provider="c"
          :specs="specsOf(c)"
          :api="api()"
          :auto-open="fresh === c.id"
          @changed="changed"
          @error="error = $event"
        />
      </div>
    </section>
  </div>
</template>
