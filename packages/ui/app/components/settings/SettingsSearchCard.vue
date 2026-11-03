<script setup lang="ts">
// One connected search provider (Settings > Search): status (key, last test), Test / options / key or URL /
// enable / disconnect for whoever may change it, and which provider is in use (the default for everyone,
// the caller's own choice). Global providers are read-only for non-admins, but anyone may use them.
import type { PresetOptionView, PresetView, ProviderPatch, ProviderView, ProvidersApi, TestResult } from '~/lib/providers/types'

const props = defineProps<{
  provider: ProviderView
  preset?: PresetView
  // The options it may set (its preset's; a custom SearXNG takes the SearXNG preset's).
  specs: PresetOptionView[]
  api: ProvidersApi
  // Open the options right away (just connected).
  autoOpen?: boolean
}>()
const emit = defineEmits<{ changed: [], error: [message: string] }>()

const p = computed(() => props.provider)
const busy = ref('')
const test = ref<TestResult | null>(null)
const showOptions = ref(!!props.autoOpen && props.specs.length > 0)

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

// Key (presets with one) or URL and name (SearXNG instances).
const hasKey = computed(() => !!props.preset?.fields.some(f => f.name === 'api_key'))
const hasUrl = computed(() => !props.preset || props.preset.fields.some(f => f.name === 'base_url'))
const editing = ref(false)
const newKey = ref('')
const newUrl = ref('')
const newName = ref('')
function startEdit() {
  editing.value = !editing.value
  newKey.value = ''
  newUrl.value = p.value.baseUrl
  newName.value = props.preset ? '' : p.value.name
}

async function save(patch: ProviderPatch) {
  if (await run('save', () => props.api.update(p.value.id, patch))) {
    editing.value = false
    test.value = null
    emit('changed')
  }
}

function saveEdit() {
  const patch: ProviderPatch = {}
  if (hasKey.value && newKey.value.trim()) patch.apiKey = newKey.value.trim()
  if (hasUrl.value && newUrl.value.trim() && newUrl.value.trim() !== p.value.baseUrl) patch.baseUrl = newUrl.value.trim()
  if (!props.preset && newName.value.trim() !== p.value.name) patch.name = newName.value.trim()
  if (Object.keys(patch).length) void save(patch)
  else editing.value = false
}

// A refused change redraws the fields from the stored options.
const optionsKey = ref(0)
async function saveOptions(options: Record<string, string>) {
  if (await run('options', () => props.api.update(p.value.id, { options }))) emit('changed')
  else optionsKey.value++
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

const scopeLabel = computed(() => (p.value.scope === 'global' ? 'Everyone' : 'Only you'))
const meta = computed(() => {
  const parts: string[] = []
  if (hasUrl.value) parts.push(p.value.baseUrl)
  if (hasKey.value) parts.push(p.value.keySet ? `key ${p.value.keyHint}` : 'no key')
  const set = props.specs.filter(o => p.value.options?.[o.name]).map(o => `${o.label.toLowerCase()} ${o.choices.find(c => c.value === p.value.options![o.name])?.label.toLowerCase() ?? p.value.options![o.name]}`)
  return [...parts, ...set].join(' · ')
})
const dot = computed(() => {
  if (!p.value.enabled) return 'bg-graphite-400'
  if (test.value) return test.value.ok ? 'bg-ok' : 'bg-bad'
  return !hasKey.value || p.value.keySet ? 'bg-ok' : 'bg-warn'
})

const input = 'h-7 w-full border border-accented bg-default px-2 text-[12px] text-white outline-none focus:border-accent'
</script>

<template>
  <div
    class="flex flex-col border-t border-default"
    :aria-label="`${p.name} (${p.scope === 'global' ? 'everyone' : 'yours'})`"
    role="group"
  >
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2 py-2 pr-3 pl-[52px] text-[12px] max-sm:pl-3">
      <span class="flex min-w-[200px] flex-1 flex-col gap-0.5">
        <span class="flex items-center gap-2 text-white">
          <span
            class="size-1.5 shrink-0"
            :class="dot"
          />
          <span class="truncate">{{ preset ? scopeLabel : p.name }}</span>
          <span
            v-if="!preset"
            class="border border-default px-1 text-[10.5px] text-muted"
          >{{ scopeLabel }}</span>
          <span
            v-if="!p.enabled"
            class="border border-default px-1 text-[10.5px] text-dimmed"
          >off</span>
        </span>
        <span
          v-if="meta"
          class="truncate text-[11.5px] text-muted"
        >{{ meta }}</span>
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
          v-if="specs.length"
          size="sm"
          :aria-expanded="showOptions"
          @click="showOptions = !showOptions"
        >
          Options
        </HBtn>
        <HBtn
          size="sm"
          :aria-expanded="editing"
          @click="startEdit"
        >
          {{ hasKey && !hasUrl ? (p.keySet ? 'Replace key' : 'Set key') : 'Edit' }}
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
      v-if="editing"
      class="flex flex-wrap gap-2 pr-3 pb-2.5 pl-[52px] max-sm:pl-3"
      @submit.prevent="saveEdit"
    >
      <input
        v-if="!preset"
        v-model="newName"
        type="text"
        :aria-label="`Name of ${p.name}`"
        placeholder="Name"
        :class="[input, 'max-w-[180px]']"
      >
      <input
        v-if="hasUrl"
        v-model="newUrl"
        type="url"
        :aria-label="`Instance URL of ${p.name}`"
        placeholder="https://searx.example.com"
        :class="[input, 'min-w-[220px] flex-1']"
      >
      <input
        v-if="hasKey"
        v-model="newKey"
        type="password"
        autocomplete="off"
        :aria-label="`New API key for ${p.name}`"
        placeholder="New API key"
        :class="[input, 'min-w-[220px] flex-1']"
      >
      <HBtn
        size="sm"
        type="submit"
        variant="primary"
        :disabled="busy === 'save'"
      >
        Save
      </HBtn>
    </form>

    <SettingsSearchChoice
      :provider-id="p.id"
      :name="p.name"
      :shared="p.scope === 'global'"
      :enabled="p.enabled"
    />

    <SettingsSearchOptions
      v-if="showOptions && p.editable && specs.length"
      :key="optionsKey"
      :specs="specs"
      :values="p.options ?? {}"
      :name="p.name"
      :disabled="busy === 'options'"
      @save="saveOptions"
    />
  </div>
</template>
