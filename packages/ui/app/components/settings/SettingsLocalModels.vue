<script setup lang="ts">
// Settings > Models, "On this computer": model servers running on this machine (Ollama, LM Studio, llama.cpp,
// MLX, vLLM, Jan, custom OpenAI-compatible endpoints) that the client calls directly. Found on their default
// ports; custom endpoints are added here. Everything stays on this computer: nothing is synced. Ollama models can
// be pulled and deleted here.
import type { LocalModel, LocalModelsApi, LocalProvider, LocalProvidersState } from '~/lib/transport/localModels'

const store = useSessionStore()
const state = ref<LocalProvidersState>({ providers: [], disabledByPolicy: false })
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')

function api(): LocalModelsApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.localModels()
}
function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}
async function run(fn: () => Promise<unknown>) {
  error.value = ''
  try {
    await fn()
  } catch (e) {
    error.value = message(e)
  }
}

async function load(refresh = false) {
  refreshing.value = refresh
  await run(async () => {
    state.value = await api().list({ refresh })
  })
  loading.value = false
  refreshing.value = false
  // New models join the pickers.
  if (refresh && store.transport) store.models = await store.transport.listModels()
}
onMounted(() => load())

const reachable = computed(() => state.value.providers.filter(p => p.reachable || !p.builtin))
const offline = computed(() => state.value.providers.filter(p => !p.reachable && p.builtin))

const toggle = (p: LocalProvider) => run(async () => {
  await api().update(p.id, { enabled: !p.enabled })
  await load(true)
})
const remove = (p: LocalProvider) => run(async () => {
  await api().remove(p.id)
  await load(true)
})

// Custom endpoint.
const adding = ref(false)
const draft = reactive({ name: '', baseUrl: 'http://', apiKey: '' })
const add = () => run(async () => {
  await api().add({ kind: 'custom', name: draft.name.trim() || undefined, baseUrl: draft.baseUrl.trim(), apiKey: draft.apiKey.trim() || undefined })
  adding.value = false
  Object.assign(draft, { name: '', baseUrl: 'http://', apiKey: '' })
  await load(true)
})

// Ollama pulls.
const pullName = ref('')
const pulling = ref<{ provider: string, model: string, status: string, pct: number } | null>(null)
let pullHandle: { cancel(): void } | null = null
function pull(p: LocalProvider) {
  const model = pullName.value.trim()
  if (!model || pulling.value) return
  pulling.value = { provider: p.id, model, status: 'starting', pct: 0 }
  const h = api().pullOllamaModel(model, {
    onProgress: (pr) => {
      if (pulling.value) pulling.value = { ...pulling.value, status: pr.status, pct: pr.total ? pr.completed / pr.total : pulling.value.pct }
    }
  }, p.id)
  pullHandle = h
  h.done.then(() => {
    pullName.value = ''
    return load(true)
  }).catch((e) => {
    error.value = message(e)
  }).finally(() => {
    pulling.value = null
    pullHandle = null
  })
}
onBeforeUnmount(() => pullHandle?.cancel())
const deleteModel = (p: LocalProvider, m: LocalModel) => run(async () => {
  await api().deleteOllamaModel(m.name, p.id)
  await load(true)
})

const gb = (n: number) => n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : `${Math.max(1, Math.round(n / 1e6))} MB`
const ctx = (n: number) => n >= 1000 ? `${Math.round(n / 1000)}k context` : ''
function details(m: LocalModel) {
  return [m.parameterSize, m.quantization, m.sizeBytes ? gb(m.sizeBytes) : '', ctx(m.contextWindow), m.tools === false ? 'no tools' : '', m.vision ? 'vision' : '', m.thinking ? 'thinking' : ''].filter(Boolean).join(' · ')
}
const input = 'h-8 min-w-0 border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <section
    :aria-label="tt('Models on this computer')"
    class="mb-8 flex flex-col"
  >
    <div class="mb-1 flex items-center gap-2">
      <h3 class="m-0 flex-1 text-[13px] font-semibold text-strong">
        {{ tt('On this computer') }}
      </h3>
      <HBtn
        size="sm"
        :disabled="refreshing || state.disabledByPolicy"
        @click="load(true)"
      >
        {{ refreshing ? tt('Looking…') : tt('Look again') }}
      </HBtn>
    </div>
    <p class="m-0 mb-3 text-[12px] text-muted">
      {{ tt('Model servers running on this machine, called directly by this computer. Free, offline, and never synced to your other devices.') }}
    </p>

    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 flex items-start gap-2 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      <span class="flex-1">{{ error }}</span>
      <HIconBtn
        icon="x"
        :label="tt('Dismiss')"
        :size="26"
        :icon-size="12"
        @click="error = ''"
      />
    </p>
    <p
      v-if="loading"
      class="m-0 py-2 text-[12.5px] text-muted"
    >
      {{ tt('Looking for model servers…') }}
    </p>
    <p
      v-else-if="state.disabledByPolicy"
      class="m-0 border border-default px-3 py-2.5 text-[12px] text-muted"
    >
      {{ tt('Your administrator turned off local models.') }}
    </p>

    <div
      v-else
      class="overflow-hidden border border-default"
    >
      <div
        v-for="(p, i) in reachable"
        :key="p.id"
        class="flex flex-col"
        :class="i && 'border-t border-default'"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            :text="p.name.slice(0, 2).toUpperCase()"
            :dim="!p.enabled || !p.reachable"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ p.name }}
              <span
                class="text-[11px]"
                :class="p.reachable ? 'text-ok' : 'text-muted'"
              >{{ !p.enabled ? 'off' : p.reachable ? 'running' : 'not running' }}</span>
              <span
                v-if="p.version"
                class="text-[11px] text-muted"
              >{{ p.version }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ p.baseUrl }}<template v-if="!p.reachable && p.error">
              · {{ p.error }}
            </template></span>
          </span>
          <HBtn
            v-if="!p.builtin"
            size="sm"
            tone="danger"
            @click="remove(p)"
          >
            {{ tt('Remove') }}
          </HBtn>
          <HToggle
            :model-value="p.enabled"
            :label="tt('Use {0}', [p.name])"
            @update:model-value="toggle(p)"
          />
        </div>

        <ul
          v-if="p.enabled && p.models.length"
          class="m-0 flex list-none flex-col border-t border-default p-0"
        >
          <li
            v-for="m in p.models"
            :key="m.id"
            class="flex items-center gap-3 py-2 pr-3 pl-[52px] text-[12px]"
          >
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="truncate text-strong">{{ m.displayName || m.name }}</span>
              <span class="truncate text-[11.5px] text-muted">{{ details(m) }}</span>
            </span>
            <HBtn
              v-if="p.kind === 'ollama'"
              size="sm"
              tone="danger"
              @click="deleteModel(p, m)"
            >
              {{ tt('Delete') }}
            </HBtn>
          </li>
        </ul>

        <form
          v-if="p.kind === 'ollama' && p.enabled && p.reachable"
          class="flex items-center gap-2 border-t border-default py-2 pr-3 pl-[52px]"
          @submit.prevent="pull(p)"
        >
          <template v-if="pulling?.provider === p.id">
            <span class="min-w-0 flex-1 truncate text-[12px] text-muted">{{ pulling.model }} · {{ pulling.status }}</span>
            <span class="flex w-[120px] shrink-0 items-center gap-2 text-[11px] text-muted tabular-nums">
              <span class="h-1 flex-1 bg-raised">
                <span
                  class="block h-full bg-accent"
                  :style="{ width: `${Math.round(pulling.pct * 100)}%` }"
                />
              </span>{{ Math.round(pulling.pct * 100) }}%
            </span>
          </template>
          <template v-else>
            <input
              v-model="pullName"
              :placeholder="tt('Model to pull, e.g. qwen3:8b')"
              :aria-label="tt('Ollama model to pull')"
              autocapitalize="off"
              autocorrect="off"
              spellcheck="false"
              :class="[input, 'flex-1']"
            >
            <HBtn
              size="md"
              type="submit"
              :disabled="!pullName.trim()"
            >
              {{ tt('Pull') }}
            </HBtn>
          </template>
        </form>
      </div>

      <!-- Custom endpoint -->
      <div
        class="flex flex-col"
        :class="reachable.length && 'border-t border-default'"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            icon="plus"
            dim
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="text-[12.5px] text-strong">{{ tt('Custom endpoint') }}</span>
            <span class="truncate text-[11.5px] text-muted">{{ tt('Any OpenAI-compatible server, on this machine or your network.') }}</span>
          </span>
          <HBtn
            v-if="!adding"
            size="sm"
            @click="adding = true"
          >
            <HIcon
              name="plus"
              :size="12"
            />{{ tt('Add') }}
          </HBtn>
        </div>
        <form
          v-if="adding"
          class="flex flex-col gap-2 border-t border-default px-3 py-2.5 pl-[52px]"
          @submit.prevent="add"
        >
          <div class="flex gap-2 max-sm:flex-col">
            <input
              v-model="draft.name"
              :placeholder="tt('Name')"
              :aria-label="tt('Name')"
              :class="[input, 'w-[160px] max-sm:w-full']"
            >
            <input
              v-model="draft.baseUrl"
              placeholder="http://192.168.1.10:8000/v1"
              :aria-label="tt('Base URL')"
              autocapitalize="off"
              spellcheck="false"
              :class="[input, 'flex-1']"
            >
          </div>
          <input
            v-model="draft.apiKey"
            type="password"
            autocomplete="off"
            :placeholder="tt('API key (optional, kept on this computer)')"
            :aria-label="tt('API key')"
            :class="input"
          >
          <div class="flex justify-end gap-2">
            <HBtn
              size="md"
              @click="adding = false"
            >
              {{ tt('Cancel') }}
            </HBtn>
            <HBtn
              size="md"
              variant="primary"
              type="submit"
              :disabled="!/^https?:\/\/.+/.test(draft.baseUrl.trim())"
            >
              {{ tt('Add') }}
            </HBtn>
          </div>
        </form>
      </div>

      <p
        v-if="offline.length"
        class="m-0 border-t border-default px-3 py-2 text-[11.5px] text-muted"
      >
        {{ tt('Not running: {0}. Start one and press Look again.', [offline.map(p => p.name).join(', ')]) }}
      </p>
    </div>
  </section>
</template>
