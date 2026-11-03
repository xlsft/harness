<script setup lang="ts">
// Settings > Providers: the LLM providers whose models the server offers. Global providers (every user;
// admins edit them) and the user's own ("Yours", with their own API key; only they see and use them).
// Keys are write-only: the form sends a new key or leaves the stored one alone. Admins also get the
// auto-mode classifier block. Changes refresh the model catalog, so the Models section follows.
import type { ModelCandidateView, ProviderForm, ProviderList, ProviderScopeName, ProviderView, ProvidersApi } from '~/lib/providers/types'
import { PROVIDER_TYPE_LABELS, PROVIDER_TYPE_URLS } from '~/lib/providers/types'

const emit = defineEmits<{ changed: [list: ProviderList] }>()
const store = useSessionStore()

const list = ref<ProviderList>({ providers: [], types: [], canManageGlobal: false })
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
    list.value = await api().list()
    error.value = ''
    emit('changed', list.value)
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// Reloads the providers and the model catalog after a change.
async function changed() {
  await load()
  try {
    if (store.transport) store.models = await store.transport.listModels()
  } catch {
    // The catalog refreshes on the next start.
  }
}

async function run(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
  } catch (e) {
    error.value = message(e)
    return false
  }
  return true
}

const global = computed(() => list.value.providers.filter(p => p.scope === 'global'))
const mine = computed(() => list.value.providers.filter(p => p.scope === 'personal'))
const groups = computed(() => [
  { key: 'global' as const, label: 'Global', items: global.value, canAdd: list.value.canManageGlobal },
  { key: 'personal' as const, label: 'Yours', items: mine.value, canAdd: true }
])
const types = computed(() => list.value.types.length ? list.value.types : Object.keys(PROVIDER_TYPE_LABELS))

// --- Create / edit form ---------------------------------------------------------------------------

const form = ref<ProviderForm | null>(null)
const saving = ref(false)

function startCreate(scope: ProviderScopeName) {
  const type = types.value[0] ?? 'anthropic'
  form.value = { id: '', scope, newId: '', type, name: '', baseUrl: PROVIDER_TYPE_URLS[type] ?? '', apiKey: '', clearKey: false, allowPrivateNetwork: false }
}

function startEdit(p: ProviderView) {
  form.value = { id: p.id, scope: p.scope, newId: p.id, type: p.type, name: p.name === p.id ? '' : p.name, baseUrl: p.baseUrl, apiKey: '', clearKey: false, allowPrivateNetwork: p.allowPrivateNetwork }
}

function onType(type: string) {
  if (!form.value) return
  // Follow the type's usual URL while the field still holds another type's default.
  if (!form.value.baseUrl || Object.values(PROVIDER_TYPE_URLS).includes(form.value.baseUrl)) form.value.baseUrl = PROVIDER_TYPE_URLS[type] ?? ''
  form.value.type = type
}

async function save() {
  const f = form.value
  if (!f) return
  saving.value = true
  const ok = await run(async () => {
    if (f.id) {
      await api().update(f.id, {
        type: f.type,
        name: f.name,
        baseUrl: f.baseUrl,
        apiKey: f.clearKey ? '' : f.apiKey || undefined,
        allowPrivateNetwork: f.scope === 'global' ? f.allowPrivateNetwork : undefined
      })
    } else {
      await api().create({ scope: f.scope, id: f.newId, type: f.type, name: f.name, baseUrl: f.baseUrl, apiKey: f.apiKey, allowPrivateNetwork: f.allowPrivateNetwork })
    }
  })
  saving.value = false
  if (ok) {
    form.value = null
    await changed()
  }
}

async function toggle(p: ProviderView) {
  if (await run(() => api().update(p.id, { enabled: !p.enabled }))) await changed()
}

const confirming = ref('')
async function remove(p: ProviderView) {
  if (confirming.value !== p.id) {
    confirming.value = p.id
    return
  }
  confirming.value = ''
  if (await run(() => api().remove(p.id))) await changed()
}

// --- Models ---------------------------------------------------------------------------------------

const discovered = ref<Record<string, ModelCandidateView[]>>({})
const discovering = ref('')
const filter = ref<Record<string, string>>({})
const manual = ref<Record<string, string>>({})

async function discover(p: ProviderView) {
  discovering.value = p.id
  await run(async () => {
    discovered.value = { ...discovered.value, [p.id]: await api().discover(p.id) }
  })
  discovering.value = ''
}

function closeDiscovered(id: string) {
  const { [id]: _closed, ...rest } = discovered.value
  discovered.value = rest
}

function candidates(p: ProviderView) {
  const q = (filter.value[p.id] ?? '').toLowerCase()
  return (discovered.value[p.id] ?? []).filter(c => !q || c.upstream.toLowerCase().includes(q) || c.name.toLowerCase().includes(q)).slice(0, 50)
}

async function addModel(p: ProviderView, upstream: string, name?: string, contextWindow?: number) {
  if (!upstream.trim()) return
  if (await run(() => api().addModel(p.id, { upstream: upstream.trim(), name, contextWindow }))) {
    manual.value = { ...manual.value, [p.id]: '' }
    const c = discovered.value[p.id]
    if (c) discovered.value = { ...discovered.value, [p.id]: c.map(x => x.upstream === upstream.trim() ? { ...x, modelIds: [...x.modelIds, upstream] } : x) }
    await changed()
  }
}

async function removeModel(id: string) {
  if (await run(() => api().removeModel(id))) await changed()
}

function ctx(n: number) {
  if (!n) return ''
  return n >= 1_000_000 ? `${n / 1_000_000}M` : `${Math.round(n / 1000)}k`
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-white outline-none focus:border-accent'
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      Providers
    </h2>
    <p class="set-sub">
      Where models come from. Global providers serve everyone on this server; yours use your own API key and only you see their models.
    </p>

    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <p
      v-if="loading"
      class="m-0 py-4 text-[12.5px] text-muted"
    >
      Loading…
    </p>

    <template v-else>
      <section
        v-for="group in groups"
        :key="group.key"
        :aria-label="`${group.label} providers`"
        class="mb-6 flex flex-col"
      >
        <div class="flex items-center gap-2 pb-2 text-[12px] text-muted">
          <span class="text-white">{{ group.label }}</span>
          <span>{{ group.key === 'global' ? (list.canManageGlobal ? 'every user · you manage them' : 'every user · managed by admins') : 'only you' }}</span>
          <span class="flex-1" />
          <HBtn
            v-if="group.canAdd && !(form && !form.id && form.scope === group.key)"
            size="sm"
            @click="startCreate(group.key)"
          >
            <HIcon
              name="plus"
              :size="12"
            />Add provider
          </HBtn>
        </div>

        <p
          v-if="group.key === 'personal'"
          class="m-0 mb-2 text-[11.5px] text-muted"
        >
          Keys are stored encrypted on the server and never shown again; admins can see that a key is set but cannot read or use it.
        </p>

        <form
          v-if="form && !form.id && form.scope === group.key"
          class="mb-2 flex flex-col gap-3 border border-default p-3"
          :aria-label="`New ${group.label.toLowerCase()} provider`"
          @submit.prevent="save"
        >
          <SettingsProviderFields
            v-model="form"
            :types="types"
            :input="input"
            @type="onType"
          />
          <div class="flex justify-end gap-2">
            <HBtn
              size="md"
              variant="ghost"
              tone="muted"
              @click="form = null"
            >
              Cancel
            </HBtn>
            <HBtn
              size="md"
              variant="primary"
              type="submit"
              :disabled="saving || !form.baseUrl || (form.scope === 'global' && !form.newId)"
            >
              Add
            </HBtn>
          </div>
        </form>

        <div
          v-if="group.items.length"
          class="border border-default"
        >
          <div
            v-for="p in group.items"
            :key="p.id"
            class="flex flex-col border-b border-default last:border-b-0"
          >
            <form
              v-if="form && form.id === p.id"
              class="flex flex-col gap-3 p-3"
              :aria-label="`Edit ${p.name}`"
              @submit.prevent="save"
            >
              <SettingsProviderFields
                v-model="form"
                :types="types"
                :input="input"
                :key-hint="p.keySet ? p.keyHint : ''"
                @type="onType"
              />
              <div class="flex justify-end gap-2">
                <HBtn
                  size="md"
                  variant="ghost"
                  tone="muted"
                  @click="form = null"
                >
                  Cancel
                </HBtn>
                <HBtn
                  size="md"
                  variant="primary"
                  type="submit"
                  :disabled="saving || !form.baseUrl"
                >
                  Save
                </HBtn>
              </div>
            </form>

            <template v-else>
              <div class="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5 text-[12.5px]">
                <span class="flex min-w-[220px] flex-1 flex-col gap-0.5">
                  <span class="flex items-center gap-2 truncate text-white">
                    {{ p.name }}
                    <span
                      v-if="p.scope === 'global'"
                      class="border border-default px-1 text-[10.5px] text-muted"
                    >Global</span>
                    <span
                      v-if="!p.enabled"
                      class="border border-default px-1 text-[10.5px] text-dimmed"
                    >off</span>
                  </span>
                  <span class="truncate text-[11.5px] text-muted">
                    {{ [PROVIDER_TYPE_LABELS[p.type] ?? p.type, p.baseUrl, p.keySet ? `key ${p.keyHint}` : 'no key'].join(' · ') }}
                  </span>
                </span>
                <div
                  v-if="p.editable"
                  class="flex flex-wrap gap-1.5"
                >
                  <HBtn
                    size="sm"
                    :disabled="discovering === p.id"
                    @click="discover(p)"
                  >
                    {{ discovering === p.id ? 'Testing…' : 'Test & discover' }}
                  </HBtn>
                  <HBtn
                    size="sm"
                    @click="toggle(p)"
                  >
                    {{ p.enabled ? 'Disable' : 'Enable' }}
                  </HBtn>
                  <HBtn
                    size="sm"
                    @click="startEdit(p)"
                  >
                    Edit
                  </HBtn>
                  <HBtn
                    size="sm"
                    tone="danger"
                    @click="remove(p)"
                  >
                    {{ confirming === p.id ? 'Confirm delete' : 'Delete' }}
                  </HBtn>
                </div>
              </div>

              <div class="flex flex-col gap-1 px-3 pb-2.5">
                <div
                  v-for="m in p.models"
                  :key="m.id"
                  class="flex items-center gap-2 text-[11.5px]"
                >
                  <span class="truncate text-toned">{{ m.name }}</span>
                  <span class="truncate text-dimmed">{{ m.id }}{{ m.contextWindow ? ` · ${ctx(m.contextWindow)}` : '' }}</span>
                  <span class="flex-1" />
                  <HIconBtn
                    v-if="p.editable"
                    icon="x"
                    :size="26"
                    :icon-size="12"
                    :label="`Remove ${m.id}`"
                    @click="removeModel(m.id)"
                  />
                </div>
                <span
                  v-if="!p.models.length"
                  class="text-[11.5px] text-dimmed"
                >No models yet{{ p.editable ? ' — discover them or add one by its upstream id.' : '.' }}</span>

                <form
                  v-if="p.editable"
                  class="mt-1 flex gap-2"
                  @submit.prevent="addModel(p, manual[p.id] ?? '')"
                >
                  <input
                    :value="manual[p.id] ?? ''"
                    :aria-label="`Upstream model id for ${p.name}`"
                    placeholder="Upstream model id, e.g. anthropic/claude-haiku-5.5"
                    :class="input"
                    class="h-7"
                    @input="manual = { ...manual, [p.id]: ($event.target as HTMLInputElement).value }"
                  >
                  <HBtn
                    size="sm"
                    type="submit"
                    :disabled="!(manual[p.id] ?? '').trim()"
                  >
                    Add model
                  </HBtn>
                </form>

                <div
                  v-if="discovered[p.id]"
                  class="mt-2 flex flex-col gap-1 border-t border-default pt-2"
                >
                  <div class="flex items-center gap-2 text-[11.5px] text-muted">
                    <span>{{ discovered[p.id]!.length }} models offered</span>
                    <input
                      :value="filter[p.id] ?? ''"
                      aria-label="Filter offered models"
                      placeholder="Filter"
                      :class="input"
                      class="h-6 max-w-[220px]"
                      @input="filter = { ...filter, [p.id]: ($event.target as HTMLInputElement).value }"
                    >
                    <span class="flex-1" />
                    <HIconBtn
                      icon="x"
                      label="Close the list"
                      :size="26"
                      @click="closeDiscovered(p.id)"
                    />
                  </div>
                  <div class="max-h-[240px] overflow-y-auto">
                    <div
                      v-for="c in candidates(p)"
                      :key="c.upstream"
                      class="flex items-center gap-2 py-0.5 text-[11.5px]"
                    >
                      <span class="truncate text-toned">{{ c.upstream }}</span>
                      <span class="truncate text-dimmed">{{ ctx(c.contextWindow) }}</span>
                      <span class="flex-1" />
                      <span
                        v-if="c.modelIds.length"
                        class="text-dimmed"
                      >added</span>
                      <HBtn
                        v-else
                        size="xs"
                        @click="addModel(p, c.upstream, c.name !== c.upstream ? c.name : undefined, c.contextWindow || undefined)"
                      >
                        Add
                      </HBtn>
                    </div>
                  </div>
                </div>
              </div>
            </template>
          </div>
        </div>
        <p
          v-else-if="!(form && !form.id && form.scope === group.key)"
          class="m-0 border border-default px-3 py-5 text-center text-[12.5px] text-muted"
        >
          {{ group.key === 'global' ? 'No global providers.' : 'You have not added a provider.' }}
        </p>
      </section>
    </template>

    <SettingsClassifier
      v-if="!loading && list.canManageGlobal"
      :providers="global"
    />
  </div>
</template>
