<script setup lang="ts">
// Settings > Models > Defaults: the server-wide default model per role (slots). Admins pick a global model
// or "Server default" for each row and the change is saved at once; everyone else sees the choices read-only.
// The plan slot has no row here and is kept as it is.
import type { ModelSlots, ProviderView, ProvidersApi } from '~/lib/providers/types'
import { emptySlots, slotModelOptions } from '~/lib/providers/types'

const props = defineProps<{ providers: ProviderView[], api: ProvidersApi }>()
const store = useSessionStore()

const ROWS: { key: keyof ModelSlots, label: string, desc: string }[] = [
  { key: 'main', label: 'Default model', desc: 'New sessions start with it.' },
  { key: 'title', label: 'Chat titles', desc: 'Short model is enough; falls back to the small model.' },
  { key: 'small', label: 'Background tasks', desc: 'Summaries, web fetch and compaction.' }
]

const slots = ref<ModelSlots>(emptySlots())
const editable = ref(false)
const loaded = ref(false)
const saving = ref(false)
const error = ref('')

const models = computed(() => slotModelOptions(props.providers))
// Per row: "Server default" names the model it currently resolves to.
const defaults = ref<ModelSlots>(emptySlots())
const optionsFor = (key: keyof ModelSlots) => [{ value: '', label: 'Server default', hint: name(defaults.value[key]) }, ...models.value]

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

// A slot's model by name; an id the catalog no longer has is shown as it is.
function name(id: string) {
  return id ? models.value.find(m => m.value === id)?.label ?? id : ''
}
function label(id: string, key: keyof ModelSlots) {
  if (id) return name(id)
  const d = name(defaults.value[key])
  return d ? `Server default (${d})` : 'Server default'
}

onMounted(async () => {
  try {
    const r = await props.api.getSlots()
    slots.value = r.slots
    defaults.value = r.defaults
    editable.value = r.editable
    loaded.value = true
  } catch (e) {
    error.value = message(e)
  }
})

async function set(key: keyof ModelSlots, id: string) {
  const prev = slots.value
  slots.value = { ...prev, [key]: id }
  saving.value = true
  try {
    slots.value = await props.api.setSlots(slots.value)
    error.value = ''
    // The catalog carries the effective slots (new sessions, titles).
    if (store.transport) store.models = await store.transport.listModels()
  } catch (e) {
    slots.value = prev
    error.value = message(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section
    aria-label="Defaults"
    class="mb-6 flex flex-col"
  >
    <h3 class="m-0 text-[13px] font-semibold text-white">
      Defaults
    </h3>
    <p class="m-0 mt-1 mb-2 text-[12px] text-muted">
      Models used when nothing else is chosen. Applies to every user{{ editable ? '' : '; admins change them' }}.
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <template v-if="loaded">
      <div
        v-for="row in ROWS"
        :key="row.key"
        class="set-row"
      >
        <div class="set-label min-w-0 flex-1">
          <span>{{ row.label }}</span>
          <span class="set-desc">{{ row.desc }}</span>
        </div>
        <div
          v-if="editable"
          class="w-[300px] shrink-0 max-sm:w-full"
        >
          <HSelect
            :model-value="slots[row.key]"
            :options="optionsFor(row.key)"
            :aria-label="row.label"
            :disabled="saving"
            @update:model-value="set(row.key, $event)"
          />
        </div>
        <span
          v-else
          class="text-[12.5px]"
          :class="slots[row.key] ? 'text-white' : 'text-muted'"
        >{{ label(slots[row.key], row.key) }}</span>
      </div>
    </template>
  </section>
</template>
