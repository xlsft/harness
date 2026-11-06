<script setup lang="ts">
// Settings > Audit log: who did what on this server, with filters and CSV. For admins and auditors. Events say that something
// changed, never a secret's value.
import { downloadCsv, type AuditEventView, type AuditFilter } from '~/lib/enterprise/types'

const store = useSessionStore()

const CATEGORIES = [
  { value: '', label: tt('Everything') },
  { value: 'auth', label: tt('Sign-ins') },
  { value: 'user', label: tt('Users') },
  { value: 'group', label: tt('Groups') },
  { value: 'provider', label: tt('Providers and keys') },
  { value: 'policy', label: tt('Policy') },
  { value: 'sign_in', label: tt('Sign-in settings') },
  { value: 'mfa', label: tt('Second factor') },
  { value: 'quota', label: tt('Quotas') },
  { value: 'scim', label: tt('Provisioning') },
  { value: 'device', label: tt('Devices') },
  { value: 'mcp', label: tt('MCP servers') },
  { value: 'vcs', label: tt('Code hosts') }
]
const OUTCOMES = [{ value: '', label: tt('Any result') }, { value: 'success', label: tt('Succeeded') }, { value: 'failure', label: tt('Failed') }]

const filter = ref<AuditFilter>({ action: '', actor: '', target: '', outcome: '', from: '', to: '' })
const events = ref<AuditEventView[]>([])
const next = ref('')
const loading = ref(true)
const exporting = ref(false)
const error = ref('')
const open = ref('')
const note = ref('')

function api() {
  return store.transport!.enterprise()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load(more = false) {
  loading.value = true
  try {
    const r = await api().listAudit(filter.value, more ? next.value : '')
    events.value = more ? [...events.value, ...r.events] : r.events
    next.value = r.nextPageToken
    error.value = ''
  } catch (e) {
    error.value = message(e)
  }
  loading.value = false
}
onMounted(() => load())

let timer: ReturnType<typeof setTimeout> | undefined
watch(filter, () => {
  clearTimeout(timer)
  timer = setTimeout(() => load(), 250)
}, { deep: true })
onBeforeUnmount(() => clearTimeout(timer))

async function exportCsv() {
  exporting.value = true
  try {
    const r = await api().exportAudit(filter.value)
    downloadCsv('harness-audit.csv', r.csv)
    note.value = r.truncated ? `Exported the newest ${r.rows} events: the export is limited. Narrow the dates for the rest.` : `Exported ${r.rows} events.`
    error.value = ''
  } catch (e) {
    error.value = message(e)
  }
  exporting.value = false
}

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
function details(e: AuditEventView) {
  try {
    return JSON.stringify(JSON.parse(e.details), null, 2)
  } catch {
    return e.details
  }
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'
const label = 'flex flex-col gap-1 text-[11.5px] text-muted'
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ tt('Audit log') }}
    </h2>
    <p class="set-sub">
      {{ tt('Sign-ins, changes to users, roles and policy, provider keys being set, provisioning. What changed, by whom, from where; never a password or key. Also forwarded to a SIEM when the server is set up for it.') }}
    </p>

    <div class="mb-3 grid grid-cols-3 gap-2 max-md:grid-cols-2">
      <label :class="label">{{ tt('What') }}
        <HSelect
          v-model="filter.action"
          :aria-label="tt('Category')"
          :options="CATEGORIES"
        />
      </label>
      <label :class="label">{{ tt('Result') }}
        <HSelect
          v-model="filter.outcome"
          :aria-label="tt('Result')"
          :options="OUTCOMES"
        />
      </label>
      <label :class="label">{{ tt('Who') }}
        <input
          v-model="filter.actor"
          type="search"
          :placeholder="tt('Name, email or id')"
          :class="input"
        >
      </label>
      <label :class="label">{{ tt('On what') }}
        <input
          v-model="filter.target"
          type="search"
          :placeholder="tt('Email, id or name')"
          :class="input"
        >
      </label>
      <label :class="label">{{ tt('From') }}
        <input
          v-model="filter.from"
          type="datetime-local"
          :class="input"
        >
      </label>
      <label :class="label">{{ tt('To') }}
        <input
          v-model="filter.to"
          type="datetime-local"
          :class="input"
        >
      </label>
    </div>

    <div class="mb-2 flex items-center gap-2 text-[11.5px] text-muted">
      <span
        v-if="note"
        role="status"
      >{{ note }}</span>
      <span class="flex-1" />
      <HBtn
        size="sm"
        :disabled="exporting"
        @click="exportCsv"
      >
        <HIcon
          name="download"
          :size="12"
        />{{ exporting ? tt('Exporting…') : tt('Export CSV') }}
      </HBtn>
    </div>

    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="events.length"
      class="overflow-hidden border border-default"
    >
      <div
        v-for="e in events"
        :key="e.id"
        class="flex flex-col border-b border-default last:border-b-0"
      >
        <button
          type="button"
          class="flex flex-wrap items-center gap-x-3 gap-y-0.5 rounded-none px-3 py-2 text-left text-[12px] hover:bg-hover"
          :aria-expanded="open === e.id"
          @click="open = open === e.id ? '' : e.id"
        >
          <span class="w-[160px] shrink-0 text-[11.5px] text-dimmed">{{ when(e.at) }}</span>
          <span
            class="font-mono text-[12px]"
            :class="e.outcome === 'failure' ? 'text-bad' : 'text-strong'"
          >{{ e.action }}</span>
          <span class="min-w-0 flex-1 truncate text-[11.5px] text-muted">{{ [e.actor || e.actorId, e.target || e.targetId].filter(Boolean).join(' → ') }}</span>
        </button>
        <dl
          v-if="open === e.id"
          class="m-0 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 border-t border-default px-3 py-2 text-[11.5px]"
        >
          <dt class="text-muted">
            {{ tt('Result') }}
          </dt><dd class="m-0 text-toned">
            {{ e.outcome }}
          </dd>
          <dt class="text-muted">
            {{ tt('Who') }}
          </dt><dd class="m-0 text-toned">
            {{ [e.actor, e.actorId, e.actorIp].filter(Boolean).join(' · ') || 'anonymous' }}
          </dd>
          <dt class="text-muted">
            {{ tt('On what') }}
          </dt><dd class="m-0 text-toned">
            {{ [e.targetType, e.target, e.targetId].filter(Boolean).join(' · ') || '—' }}
          </dd>
          <dt class="text-muted">
            {{ tt('Details') }}
          </dt><dd class="m-0">
            <pre class="m-0 font-mono text-[11.5px] break-all whitespace-pre-wrap text-toned">{{ details(e) }}</pre>
          </dd>
        </dl>
      </div>
    </div>
    <p
      v-else-if="!loading"
      class="m-0 border border-default px-3 py-5 text-center text-[12.5px] text-muted"
    >
      {{ tt('No events match.') }}
    </p>
    <div
      v-if="next"
      class="mt-2 flex justify-center"
    >
      <HBtn
        size="sm"
        :disabled="loading"
        @click="load(true)"
      >
        {{ tt('Older') }}
      </HBtn>
    </div>
    <SettingsDecisionLog />
  </div>
</template>
