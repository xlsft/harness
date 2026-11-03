<script setup lang="ts">
// One connected provider (Settings > Providers): status (key, last test), its models and the actions the
// caller may take. LLM providers list the models they offer (asked from the service, or the preset's curated
// list) with a switch each: on adds the model to the catalog, off removes it. Classifier providers show
// their decision models. Global providers are read-only for non-admins.
import type { ModelCandidateView, PresetView, ProtocolView, ProviderPatch, ProviderView, ProvidersApi, TestResult } from '~/lib/providers/types'
import { contextLabel } from '~/lib/providers/types'

const sessions = useSessionStore()
const { prefs, save: saveDefault, effortOf, setEffort } = useSessionDefaults()
// The model new sessions start with: the one chosen here, else the server's default.
const isDefault = (id: string) => (prefs.value.model || sessions.defaultModel || sessions.models[0]?.id) === id
function toggleDefault(id: string) {
  saveDefault('model', prefs.value.model === id ? '' : id)
}
const catalogOf = (id: string) => sessions.models.find(x => x.id === id)
// Effort levels from the model catalog (none until the model is in it).
const levelsOf = (id: string) => {
  const m = catalogOf(id)
  return m ? modelEfforts(m) : []
}

const props = defineProps<{
  provider: ProviderView
  preset?: PresetView
  protocols: ProtocolView[]
  canManageGlobal: boolean
  api: ProvidersApi
  // Open the model list right away (just connected).
  autoOpen?: boolean
}>()
const emit = defineEmits<{ changed: [], error: [message: string] }>()

const p = computed(() => props.provider)
const classifier = computed(() => p.value.group === 'classifier')
// Classifiers page (admins): whether this provider is the classifier in use; one at a time.
const classifierState = inject(CLASSIFIER, null)
const inUse = computed(() => classifierState?.settings.value?.providerId === p.value.id)
const busy = ref('')
const test = ref<TestResult | null>(null)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function run(what: string, fn: () => Promise<unknown>) {
  busy.value = what
  try {
    await fn()
    return true
  } catch (e) {
    emit('error', message(e))
    return false
  } finally {
    busy.value = ''
  }
}

async function runTest() {
  await run('test', async () => {
    test.value = await props.api.test(p.value.id)
  })
}

// --- Models ---------------------------------------------------------------------------------------

const open = ref(false)
const candidates = ref<ModelCandidateView[] | null>(null)
const filter = ref('')
const manual = ref('')

async function openModels() {
  open.value = !open.value
  if (open.value && !candidates.value) {
    await run('models', async () => {
      candidates.value = await props.api.discover(p.value.id)
    })
  }
}
onMounted(() => {
  if (props.autoOpen && p.value.editable && !classifier.value) void openModels()
})

const shown = computed(() => {
  const q = filter.value.toLowerCase()
  const list = candidates.value ?? []
  return list.filter(c => !q || c.upstream.toLowerCase().includes(q) || c.name.toLowerCase().includes(q)).slice(0, 80)
})

function enabledId(upstream: string) {
  return p.value.models.find(m => m.upstream === upstream)?.id
}

async function setModel(c: { upstream: string, name?: string, contextWindow?: number }, on: boolean) {
  const id = enabledId(c.upstream)
  const ok = await run(`m:${c.upstream}`, async () => {
    if (on && !id) await props.api.addModel(p.value.id, { upstream: c.upstream, name: c.name && c.name !== c.upstream ? c.name : undefined, contextWindow: c.contextWindow || undefined })
    else if (!on && id) await props.api.removeModel(id)
  })
  if (ok) emit('changed')
}

async function addManual() {
  const id = manual.value.trim()
  if (!id) return
  await setModel({ upstream: id }, true)
  manual.value = ''
}

// Decision models of a classifier provider (the preset's list; a custom one is set in the classifier).
const decisionModels = computed(() => props.preset?.defaultModels ?? [])

// --- Edit / key / disconnect -----------------------------------------------------------------------

const editing = ref(false)
const replacingKey = ref(false)
const newKey = ref('')

async function save(patch: ProviderPatch) {
  if (await run('save', () => props.api.update(p.value.id, patch))) {
    editing.value = false
    replacingKey.value = false
    newKey.value = ''
    test.value = null
    emit('changed')
  }
}

async function toggleEnabled() {
  if (await run('toggle', () => props.api.update(p.value.id, { enabled: !p.value.enabled }))) emit('changed')
}

const confirming = ref(false)
async function disconnect() {
  if (!confirming.value) {
    confirming.value = true
    setTimeout(() => (confirming.value = false), 4000)
    return
  }
  confirming.value = false
  if (await run('remove', () => props.api.remove(p.value.id))) emit('changed')
}

const meta = computed(() => {
  const parts: string[] = []
  if (!props.preset) parts.push(props.protocols.find(x => x.id === p.value.type)?.label ?? p.value.type)
  if (!props.preset || props.preset.local) parts.push(p.value.baseUrl)
  parts.push(p.value.keySet ? `key ${p.value.keyHint}` : 'no key')
  if (p.value.headerNames.length) parts.push(`${p.value.headerNames.length} header${p.value.headerNames.length > 1 ? 's' : ''}`)
  return parts.join(' · ')
})

const input = 'h-7 w-full border border-accented bg-default px-2 text-[12px] text-white outline-none focus:border-accent'
</script>

<template>
  <div
    class="flex flex-col border-t border-default"
    :aria-label="`${p.name} (${p.scope === 'global' ? 'everyone' : 'yours'})`"
    role="group"
  >
    <SettingsCustomForm
      v-if="editing"
      :group="p.group"
      :protocols="protocols.filter(x => x.group === p.group)"
      :can-manage-global="canManageGlobal"
      :busy="busy === 'save'"
      :provider="p"
      @update="save"
      @cancel="editing = false"
    />

    <template v-else>
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2 py-2 pr-3 pl-[52px] text-[12px] max-sm:pl-3">
        <span class="flex min-w-[200px] flex-1 flex-col gap-0.5">
          <span class="flex items-center gap-2 text-white">
            <span
              class="size-1.5 shrink-0"
              :class="!p.enabled ? 'bg-graphite-400' : test ? (test.ok ? 'bg-ok' : 'bg-bad') : p.keySet || !preset?.fields.some(f => f.required && f.name === 'api_key') ? 'bg-ok' : 'bg-warn'"
            />
            <span class="truncate">{{ preset ? (p.scope === 'global' ? 'Everyone' : 'Only you') : p.name }}</span>
            <span
              v-if="!preset"
              class="border border-default px-1 text-[10.5px] text-muted"
            >{{ p.scope === 'global' ? 'Everyone' : 'Only you' }}</span>
            <span
              v-if="!p.enabled"
              class="border border-default px-1 text-[10.5px] text-dimmed"
            >off</span>
          </span>
          <span class="truncate text-[11.5px] text-muted">{{ meta }}</span>
          <span
            v-if="test"
            role="status"
            class="text-[11.5px]"
            :class="test.ok ? 'text-ok' : 'text-bad'"
          >{{ test.message }}</span>
        </span>
        <div
          v-if="p.editable"
          class="flex flex-wrap gap-1.5"
        >
          <HBtn
            size="sm"
            :disabled="busy === 'test'"
            @click="runTest"
          >
            {{ busy === 'test' ? 'Testing…' : 'Test' }}
          </HBtn>
          <HBtn
            v-if="!classifier"
            size="sm"
            :aria-expanded="open"
            @click="openModels"
          >
            Models{{ p.models.length ? ` · ${p.models.length}` : '' }}
          </HBtn>
          <HBtn
            v-if="preset"
            size="sm"
            @click="replacingKey = !replacingKey"
          >
            {{ p.keySet ? 'Replace key' : 'Set key' }}
          </HBtn>
          <HBtn
            v-else
            size="sm"
            @click="editing = true"
          >
            Edit
          </HBtn>
          <HBtn
            size="sm"
            @click="toggleEnabled"
          >
            {{ p.enabled ? 'Disable' : 'Enable' }}
          </HBtn>
          <HBtn
            size="sm"
            tone="danger"
            @click="disconnect"
          >
            {{ confirming ? 'Confirm' : 'Disconnect' }}
          </HBtn>
        </div>
        <span
          v-else
          class="text-[11.5px] text-dimmed"
        >managed by admins</span>
      </div>

      <form
        v-if="replacingKey"
        class="flex gap-2 pr-3 pb-2.5 pl-[52px] max-sm:pl-3"
        @submit.prevent="save({ apiKey: newKey.trim() })"
      >
        <input
          v-model="newKey"
          type="password"
          autocomplete="off"
          :aria-label="`New API key for ${p.name}`"
          placeholder="New API key"
          :class="input"
        >
        <HBtn
          size="sm"
          type="submit"
          variant="primary"
          :disabled="!newKey.trim() || busy === 'save'"
        >
          Save
        </HBtn>
      </form>

      <!-- Classifier: in use (one at a time; none means rules, then the model) -->
      <div
        v-if="classifier && classifierState?.settings.value && p.scope === 'global'"
        class="pr-3 pb-2 pl-[52px] max-sm:pl-3"
      >
        <button
          type="button"
          role="switch"
          :aria-checked="inUse"
          :aria-label="`Use ${p.name} as the classifier`"
          class="flex items-center gap-1.5 text-[11.5px]"
          :class="inUse ? 'text-white' : 'text-muted hover:text-white'"
          @click="classifierState.use(inUse ? '' : p.id)"
        >
          <span
            class="flex size-3.5 items-center justify-center border"
            :class="inUse ? 'border-accent bg-accent' : 'border-accented'"
          ><HIcon
            v-if="inUse"
            name="check"
            :size="10"
            class="text-white"
          /></span>
          use as classifier
        </button>
      </div>

      <!-- Classifier: its decision models -->
      <div
        v-if="classifier && decisionModels.length"
        class="flex flex-wrap items-center gap-1.5 pr-3 pb-2.5 pl-[52px] text-[11.5px] text-muted max-sm:pl-3"
      >
        Decision model{{ decisionModels.length > 1 ? 's' : '' }}:
        <span
          v-for="m in decisionModels"
          :key="m.upstream"
          class="border border-default px-1.5 font-mono text-[11px] text-toned"
        >{{ m.upstream }}</span>
      </div>

      <!-- LLM: enabled models, and the offered ones when open -->
      <div
        v-if="!classifier && (p.models.length || open)"
        class="flex flex-col pr-3 pb-2.5 pl-[52px] max-sm:pl-3"
      >
        <!-- Enabled models with their default effort for new sessions (levels the model offers). -->
        <div
          v-if="!open"
          class="flex flex-col border border-default"
        >
          <div
            v-for="m in p.models"
            :key="m.id"
            class="flex min-h-8 items-center gap-3 border-b border-default px-2.5 text-[12px] last:border-b-0"
          >
            <span
              class="min-w-0 flex-1 truncate text-white"
              :title="m.id"
            >{{ m.name }}</span>
            <!-- One model is the default for new sessions (this browser); none set means the server's. -->
            <button
              type="button"
              role="switch"
              :aria-checked="isDefault(m.id)"
              :aria-label="`Use ${m.name} by default`"
              class="flex shrink-0 items-center gap-1.5 text-[11px]"
              :class="isDefault(m.id) ? 'text-white' : 'text-muted hover:text-white'"
              @click="toggleDefault(m.id)"
            >
              <span
                class="flex size-3.5 items-center justify-center border"
                :class="isDefault(m.id) ? 'border-accent bg-accent' : 'border-accented'"
              ><HIcon
                v-if="isDefault(m.id)"
                name="check"
                :size="10"
                class="text-white"
              /></span>
              default
            </button>
            <div
              v-if="levelsOf(m.id).length"
              role="radiogroup"
              :aria-label="`Default effort for ${m.name}`"
              class="flex gap-[2px]"
            >
              <button
                v-for="e in levelsOf(m.id)"
                :key="e"
                type="button"
                role="radio"
                :aria-checked="effortOf(catalogOf(m.id)!) === e"
                class="border px-1.5 py-px text-[11px]"
                :class="effortOf(catalogOf(m.id)!) === e ? 'border-line bg-raised text-white' : 'border-transparent text-muted hover:text-white'"
                @click="setEffort(m.id, e)"
              >
                {{ e }}
              </button>
            </div>
            <span
              v-else
              class="text-[11px] text-dimmed"
            >no effort levels</span>
          </div>
        </div>

        <div
          v-else
          class="flex flex-col gap-1.5 border border-default p-2"
        >
          <div class="flex items-center gap-2 text-[11.5px] text-muted">
            <span v-if="busy === 'models'">Asking the service for its models…</span>
            <span v-else-if="candidates">{{ candidates.length }} {{ preset && !preset.discoversModels ? 'models in the preset' : 'models offered' }} · {{ p.models.length }} on</span>
            <span class="flex-1" />
            <input
              v-if="(candidates?.length ?? 0) > 10"
              v-model="filter"
              aria-label="Filter models"
              placeholder="Filter"
              :class="input"
              class="max-w-[200px]"
            >
          </div>
          <div class="max-h-[260px] overflow-y-auto">
            <label
              v-for="c in shown"
              :key="c.upstream"
              class="flex cursor-pointer items-center gap-2 py-[3px] text-[11.5px] hover:bg-hover"
            >
              <HToggle
                :model-value="!!enabledId(c.upstream)"
                :label="`Use ${c.upstream}`"
                @update:model-value="setModel(c, $event)"
              />
              <span class="truncate text-toned">{{ c.name && c.name !== c.upstream ? c.name : c.upstream }}</span>
              <span
                v-if="c.name && c.name !== c.upstream"
                class="truncate font-mono text-[11px] text-dimmed"
              >{{ c.upstream }}</span>
              <span class="flex-1" />
              <span class="text-dimmed">{{ contextLabel(c.contextWindow) }}</span>
            </label>
            <!-- Enabled models the list does not offer (added by id). -->
            <label
              v-for="m in p.models.filter(m => !(candidates ?? []).some(c => c.upstream === m.upstream))"
              :key="m.id"
              class="flex items-center gap-2 py-[3px] text-[11.5px]"
            >
              <HToggle
                :model-value="true"
                :label="`Use ${m.upstream}`"
                @update:model-value="setModel(m, $event)"
              />
              <span class="truncate text-toned">{{ m.name }}</span>
              <span class="truncate font-mono text-[11px] text-dimmed">{{ m.upstream }}</span>
            </label>
          </div>
          <form
            class="flex gap-2"
            @submit.prevent="addManual"
          >
            <input
              v-model="manual"
              :aria-label="`Model id for ${p.name}`"
              placeholder="Another model by its id"
              :class="input"
            >
            <HBtn
              size="sm"
              type="submit"
              :disabled="!manual.trim()"
            >
              Add
            </HBtn>
          </form>
        </div>
      </div>
    </template>
  </div>
</template>
