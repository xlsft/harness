<script setup lang="ts">
// Add or edit an MCP server. The client and the server validate; this form only turns its fields into the message.
// Secret values are write-only: stored ones are listed by name and can be replaced or removed, never read.
import { draftOf, emptyDraft, McpServerSource, secretChangesOf, serverFrom, serverOf, sourcesFor, TRANSPORT_LABELS, type McpApi, type McpDraft, type McpServerEntry } from '~/lib/mcp/types'
import type { ConnectorOption } from '~/lib/extensions/types'

const props = defineProps<{ entry: McpServerEntry | null, canManageOrg: boolean, api: McpApi, initial?: ConnectorOption | null }>()
const emit = defineEmits<{ saved: [], cancel: [] }>()

function firstDraft(): McpDraft {
  if (props.entry?.server) return draftOf(props.entry.server)
  const offered = props.initial?.server
  if (!offered) return emptyDraft()
  // Discover says what it needs; the values are the user's to fill in.
  const needed = (props.initial?.required ?? []).filter(r => r.secret).map(r => `${r.name}=`).join('\n')
  return { ...draftOf(serverFrom(offered)), secrets: needed }
}
const draft = ref<McpDraft>(firstDraft())
const source = ref<McpServerSource>(props.initial?.suggestedSource || McpServerSource.DEVICE)
const error = ref('')
const saving = ref(false)
const more = ref(false)

const editing = computed(() => !!props.entry)
const local = computed(() => (props.entry ? props.entry.source === McpServerSource.DEVICE : source.value === McpServerSource.DEVICE))
const transports = computed(() => Object.entries(TRANSPORT_LABELS).map(([value, label]) => ({ value, label })))
const sources = computed(() => sourcesFor(draft.value.transport, props.canManageOrg))
const sourceOptions = computed(() => sources.value.map(o => ({ value: String(o.value), label: o.label })))
const sourceValue = computed({ get: () => String(source.value), set: (v: string) => (source.value = Number(v) as McpServerSource) })
// The destination must be one this transport can have.
watch([() => draft.value.transport, () => props.canManageOrg], () => {
  if (!sources.value.some(o => o.value === source.value)) source.value = sources.value[0]!.value
}, { immediate: true })

const stored = computed(() => props.entry?.server?.secrets ?? [])
function toggleRemoval(name: string) {
  const list = draft.value.removeSecrets
  draft.value.removeSecrets = list.includes(name) ? list.filter(n => n !== name) : [...list, name]
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const secrets = secretChangesOf(draft.value)
    if (props.entry?.server) {
      const r = await props.api.update(serverOf(draft.value, props.entry.server), secrets, props.entry.server.version)
      if (r.conflict?.server) {
        throw new Error('Someone else changed this server in the meantime. Close the form and open it again to see the change.')
      }
    } else {
      await props.api.create(source.value, serverOf(draft.value), secrets)
    }
    emit('saved')
  } catch (e) {
    error.value = message(e)
  } finally {
    saving.value = false
  }
}

const input = 'h-8 min-w-0 border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent'
const area = 'min-h-16 min-w-0 border border-accented bg-default px-2.5 py-1.5 font-mono text-[12px] text-strong outline-none focus:border-accent'
</script>

<template>
  <form
    class="flex flex-col gap-3 border border-default p-3 text-[12px]"
    :aria-label="editing ? `Edit ${entry?.server?.name}` : 'Add an MCP server'"
    @submit.prevent="save"
  >
    <p
      v-if="error"
      role="alert"
      class="m-0 border border-bad px-3 py-2 text-bad"
    >
      {{ error }}
    </p>
    <label class="flex flex-col gap-1">
      <span class="text-muted">Name</span>
      <input
        v-model="draft.name"
        :class="input"
        required
        autocomplete="off"
        spellcheck="false"
      >
    </label>
    <div class="flex flex-col gap-1">
      <span class="text-muted">How to reach it</span>
      <HSelect
        v-model="draft.transport"
        :options="transports"
        :disabled="editing"
        aria-label="Transport"
      />
    </div>
    <div
      v-if="!editing"
      class="flex flex-col gap-1"
    >
      <span class="text-muted">Where it goes</span>
      <HSelect
        v-model="sourceValue"
        :options="sourceOptions"
        aria-label="Where the server goes"
      />
      <span
        v-if="local"
        class="text-[11.5px] text-muted"
      >Stays on this computer; nothing about it goes to the xlsft`s harness server. It can reach localhost, and you approve it by adding it.</span>
    </div>

    <template v-if="draft.transport === 'stdio'">
      <label class="flex flex-col gap-1">
        <span class="text-muted">Command</span>
        <input
          v-model="draft.command"
          :class="input"
          required
          spellcheck="false"
          placeholder="npx"
        >
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">Arguments, one per line</span>
        <textarea
          v-model="draft.args"
          :class="area"
          spellcheck="false"
        />
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">Environment, NAME=value per line. Refer to secrets as ${secret:NAME}</span>
        <textarea
          v-model="draft.env"
          :class="area"
          spellcheck="false"
        />
      </label>
    </template>
    <template v-else>
      <label class="flex flex-col gap-1">
        <span class="text-muted">URL</span>
        <input
          v-model="draft.url"
          :class="input"
          required
          spellcheck="false"
          placeholder="https://example.com/mcp"
        >
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">Headers, Name: value per line. Refer to secrets as ${secret:NAME}</span>
        <textarea
          v-model="draft.headers"
          :class="area"
          spellcheck="false"
        />
      </label>
      <label
        v-if="local"
        class="flex flex-col gap-1"
      >
        <span class="text-muted">Headers helper: a command on this device that prints a JSON object of headers</span>
        <input
          v-model="draft.headersHelper"
          :class="input"
          spellcheck="false"
        >
      </label>
    </template>

    <div class="flex flex-col gap-1">
      <span class="text-muted">Secrets to set, NAME=value per line. They are stored encrypted and never shown again</span>
      <textarea
        v-model="draft.secrets"
        :class="area"
        spellcheck="false"
        autocomplete="off"
      />
      <span
        v-for="s in stored"
        :key="s.name"
        class="flex items-center gap-2 text-muted"
      >
        <span :class="draft.removeSecrets.includes(s.name) && 'line-through'">{{ s.name }}</span>
        <span>{{ s.hasValue ? 'set' : 'not set' }}</span>
        <HBtn
          v-if="s.hasValue"
          size="xs"
          @click="toggleRemoval(s.name)"
        >
          {{ draft.removeSecrets.includes(s.name) ? 'Keep' : 'Remove' }}
        </HBtn>
      </span>
    </div>

    <button
      type="button"
      class="cursor-pointer self-start border-0 bg-transparent p-0 text-[12px] text-muted underline"
      :aria-expanded="more"
      @click="more = !more"
    >
      {{ more ? 'Fewer options' : 'More options' }}
    </button>
    <template v-if="more">
      <div class="flex items-center justify-between gap-3">
        <span class="text-muted">Always load its tools instead of deferring them behind tool search</span>
        <HToggle
          v-model="draft.alwaysLoad"
          label="Always load its tools"
        />
      </div>
      <div
        v-if="draft.transport !== 'stdio'"
        class="flex items-center justify-between gap-3"
      >
        <span class="text-muted">Ask the user before every call, whatever the permission mode</span>
        <HToggle
          v-model="draft.requiresInteraction"
          label="Ask before every call"
        />
      </div>
      <label class="flex flex-col gap-1">
        <span class="text-muted">Time limit of one tool call, in seconds (default 600)</span>
        <input
          v-model="draft.toolTimeout"
          :class="input"
          inputmode="numeric"
        >
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">Largest result given to the model, in characters (default 100000)</span>
        <input
          v-model="draft.maxOutputChars"
          :class="input"
          inputmode="numeric"
        >
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">Only these tools exist, names or patterns with * (empty: all)</span>
        <input
          v-model="draft.allowedTools"
          :class="input"
          spellcheck="false"
        >
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">These tools do not exist</span>
        <input
          v-model="draft.deniedTools"
          :class="input"
          spellcheck="false"
        >
      </label>
      <template v-if="draft.transport !== 'stdio' && !local">
        <label class="flex flex-col gap-1">
          <span class="text-muted">OAuth client id, when the provider has no dynamic registration</span>
          <input
            v-model="draft.oauthClientId"
            :class="input"
            spellcheck="false"
          >
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-muted">OAuth scopes</span>
          <input
            v-model="draft.oauthScopes"
            :class="input"
            spellcheck="false"
          >
        </label>
      </template>
    </template>

    <div class="flex items-center gap-2">
      <HBtn
        type="submit"
        size="sm"
        variant="primary"
        :disabled="saving"
      >
        {{ editing ? 'Save' : 'Add server' }}
      </HBtn>
      <HBtn
        size="sm"
        :disabled="saving"
        @click="emit('cancel')"
      >
        Cancel
      </HBtn>
    </div>
  </form>
</template>
