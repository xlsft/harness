<script setup lang="ts">
// Settings > Import > Claude Code. Opening it only scans: what to bring is a choice, with overwriting and secrets as
// explicit switches. Importing shows progress with Cancel, then a summary. Leaving the screen cancels a run; importing
// again goes on from where it stopped, which the client works out. Imported sessions reach the sidebar by the session
// list, which the store polls; the end of a run asks at once.
import { VList } from 'virtua/vue'
import {
  EMPTY_SELECTION,
  canImport,
  filterProjects,
  formatBytes,
  importRequest,
  manualItems,
  newSessions,
  sectionRows,
  selectAllState,
  selectionTotal,
  toggleAll,
  toggled,
  type ClaudeImportProject,
  type ClaudeImportScan,
  type ClaudeSection
} from '~/lib/import/claudeCode'
import { IDLE_RUN, importCounts, importErrors, reduceImportRun, type ImportHandle, type ImportRunEvent } from '~/lib/import/types'

defineEmits<{ back: [] }>()

const store = useSessionStore()
const scan = ref<ClaudeImportScan | null>(null)
const scanning = ref(true)
const error = ref('')
const selection = ref(EMPTY_SELECTION)
const query = ref('')
const run = ref(IDLE_RUN)
let handle: ImportHandle | null = null

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load() {
  scanning.value = true
  error.value = ''
  try {
    if (!store.transport) throw new Error('not connected to the client')
    scan.value = await store.transport.claudeImport().scan()
  } catch (e) {
    error.value = message(e)
  }
  scanning.value = false
}
onMounted(load)
onBeforeUnmount(() => handle?.cancel())

const rows = computed(() => (scan.value ? sectionRows(scan.value) : []))
const manual = computed(() => (scan.value ? manualItems(scan.value) : []))
const visible = computed(() => filterProjects(scan.value?.projects ?? [], query.value))
const allState = computed(() => selectAllState(visible.value, selection.value.projects))
const total = computed(() => (scan.value ? selectionTotal(scan.value, selection.value) : { items: 0, sessions: 0, bytes: 0 }))
const ready = computed(() => !!scan.value && canImport(scan.value, selection.value))
const settingsChosen = computed(() => selection.value.sections.has('settings'))
const totalLine = computed(() => {
  const parts = []
  if (total.value.items) parts.push(`${total.value.items} config ${total.value.items === 1 ? 'item' : 'items'}`)
  if (total.value.sessions) parts.push(`${total.value.sessions} ${total.value.sessions === 1 ? 'session' : 'sessions'} (${formatBytes(total.value.bytes)})`)
  return parts.length ? parts.join(' and ') : 'Nothing chosen yet'
})

function toggleSection(key: ClaudeSection) {
  selection.value = { ...selection.value, sections: toggled(selection.value.sections, key) }
}
function toggleProject(p: ClaudeImportProject) {
  selection.value = { ...selection.value, projects: toggled(selection.value.projects, p.path) }
}
function selectAll() {
  selection.value = { ...selection.value, projects: toggleAll(visible.value, selection.value.projects) }
}
const force = computed({ get: () => selection.value.force, set: v => (selection.value = { ...selection.value, force: v }) })
const secrets = computed({ get: () => selection.value.includeSecrets, set: v => (selection.value = { ...selection.value, includeSecrets: v }) })

const dispatch = (e: ImportRunEvent) => (run.value = reduceImportRun(run.value, e))

function start() {
  if (!store.transport || !ready.value) return
  dispatch({ type: 'start' })
  handle = store.transport.claudeImport().run(importRequest(selection.value), { onProgress: progress => dispatch({ type: 'progress', progress }) })
  handle.done
    .then(report => dispatch(report ? { type: 'report', report } : { type: 'cancelled' }))
    .catch(e => dispatch({ type: 'failed', error: message(e) }))
    .finally(() => {
      handle = null
      store.quietly(store.refreshList())
    })
}

// Back to the choices, with what has come since marked.
function again() {
  run.value = IDLE_RUN
  selection.value = EMPTY_SELECTION
  void load()
}

const percent = computed(() => (run.value.total ? Math.round((run.value.done / run.value.total) * 100) : 0))
const counts = computed(() => importCounts(run.value))
const errors = computed(() => importErrors(run.value))
const summary = computed(() => {
  const c = counts.value
  return [
    [c.imported, 'imported'],
    [c.exists, 'already there'],
    [c.queued, 'waiting to upload'],
    [c.skipped, 'skipped'],
    [c.manual, 'to set up by hand'],
    [c.errors, c.errors === 1 ? 'error' : 'errors']
  ].filter(([n]) => n).map(([n, label]) => `${n} ${label}`).join(', ') || 'Nothing was imported.'
})

function projectLine(p: ClaudeImportProject) {
  const fresh = newSessions(p).length
  const sessions = `${p.sessionCount} ${p.sessionCount === 1 ? 'session' : 'sessions'}`
  const date = p.newestAt ? new Date(p.newestAt).toLocaleDateString() : ''
  const state = fresh === 0 ? 'nothing new' : p.importedCount ? `${p.importedCount} already imported` : ''
  return [sessions, formatBytes(p.totalBytes), date, state].filter(Boolean).join(' · ')
}
function itemsLine(r: { fresh: number, exists: number, items: unknown[] }) {
  if (!r.items.length) return 'None found'
  return [r.fresh && `${r.fresh} new`, r.exists && `${r.exists} already there`, r.items.length - r.fresh - r.exists > 0 && `${r.items.length - r.fresh - r.exists} skipped`].filter(Boolean).join(', ')
}
</script>

<template>
  <div class="mb-1 flex items-center gap-2">
    <button
      type="button"
      aria-label="Back to import sources"
      class="flex size-6 items-center justify-center text-muted hover:text-strong disabled:opacity-50"
      :disabled="run.phase === 'running'"
      @click="$emit('back')"
    >
      <HIcon
        name="arrow-left"
        :size="13"
      />
    </button>
    <h2 class="set-h">
      Import from Claude Code
    </h2>
  </div>

  <p
    v-if="scanning"
    class="set-sub"
  >
    Looking through Claude Code's folder…
  </p>
  <p
    v-else-if="error"
    class="set-sub text-error"
  >
    {{ error }}
  </p>
  <p
    v-else-if="scan && !scan.found"
    class="set-sub"
  >
    No Claude Code folder at {{ scan.claudeDir }} on this computer.
  </p>

  <!-- Running, and what it came to. -->
  <template v-else-if="run.phase !== 'idle'">
    <template v-if="run.phase === 'running'">
      <p class="set-sub">
        {{ run.done }} of {{ run.total || '…' }} · {{ run.current || 'Starting…' }}
      </p>
      <div
        role="progressbar"
        :aria-valuenow="percent"
        aria-valuemin="0"
        aria-valuemax="100"
        class="mb-3 h-1 w-full bg-line"
      >
        <div
          class="h-full bg-accent"
          :style="{ width: `${percent}%` }"
        />
      </div>
      <HBtn
        size="sm"
        @click="handle?.cancel()"
      >
        Cancel
      </HBtn>
    </template>
    <template v-else>
      <p class="set-sub">
        <template v-if="run.phase === 'cancelled'">
          Cancelled after {{ run.done }} of {{ run.total }}. Importing again goes on from here.
        </template>
        <template v-else-if="run.phase === 'failed'">
          Stopped: {{ run.error }}
        </template>
        <template v-else>
          Done.
        </template>
        {{ summary }}
      </p>
      <ul
        v-if="errors.length"
        aria-label="Import errors"
        class="m-0 mb-3 flex list-none flex-col gap-1 p-0 text-[12px]"
      >
        <li
          v-for="(e, i) in errors"
          :key="i"
        >
          <span class="text-strong">{{ e.kind }} {{ e.name }}</span>
          <span class="ml-2 text-muted">{{ e.note }}</span>
        </li>
      </ul>
      <HBtn
        size="sm"
        @click="again"
      >
        Back to the list
      </HBtn>
    </template>
  </template>

  <!-- The choice. -->
  <template v-else-if="scan">
    <p class="set-sub">
      Copies what you choose from {{ scan.claudeDir }} to your server. Claude Code's own files stay as they are.
    </p>
    <p
      v-if="!scan.connected"
      class="m-0 mb-3 text-[12px] text-error"
    >
      This client is not connected to a harness server, so there is nowhere to import to.
    </p>
    <p
      v-else-if="!scan.serverChecked"
      class="m-0 mb-3 text-[12px] text-muted"
    >
      The server did not answer, so some items marked new may already be there.
    </p>

    <h3 class="m-0 mb-1 text-[13px] font-semibold text-strong">
      Config
    </h3>
    <div
      v-for="r in rows"
      :key="r.key"
      class="flex items-center justify-between gap-4 py-1.5 text-[12.5px]"
    >
      <SettingsCheckSwitch
        :checked="selection.sections.has(r.key)"
        :label="r.label"
        :disabled="!r.selectable"
        @toggle="toggleSection(r.key)"
      >
        {{ r.label }}
      </SettingsCheckSwitch>
      <span class="truncate text-[11.5px] text-muted">{{ itemsLine(r) }}</span>
    </div>
    <div
      v-if="manual.length"
      class="flex items-start justify-between gap-4 py-1.5 text-[12.5px]"
    >
      <span class="text-muted">MCP servers</span>
      <span class="text-right text-[11.5px] text-muted">{{ manual.map(i => i.name).join(', ') }}: not imported, set them up by hand</span>
    </div>

    <div class="mt-4 mb-2 flex items-center gap-2">
      <h3 class="m-0 flex-1 text-[13px] font-semibold text-strong">
        Sessions
      </h3>
      <HBtn
        size="xs"
        :disabled="!visible.length"
        @click="selectAll"
      >
        {{ allState === 'all' ? 'Clear' : 'Select all' }}
      </HBtn>
    </div>
    <template v-if="scan.projects.length">
      <input
        v-model="query"
        placeholder="Search projects"
        aria-label="Search projects"
        class="mb-2 h-8 w-full border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent"
      >
      <VList
        v-if="visible.length"
        v-slot="{ item: p }"
        :data="visible"
        :item-size="44"
        class="border border-line"
        :style="{ height: '280px' }"
      >
        <div
          :key="p.folder"
          class="flex h-11 items-center gap-2 px-2.5 text-[12.5px]"
        >
          <SettingsCheckSwitch
            :checked="selection.projects.has(p.path)"
            :label="p.path"
            :disabled="newSessions(p).length === 0"
            class="min-w-0 flex-1"
            @toggle="toggleProject(p)"
          >
            <span class="flex min-w-0 flex-col text-left">
              <span class="truncate">{{ p.path }}</span>
              <span class="truncate text-[11px] text-muted">{{ projectLine(p) }}</span>
            </span>
          </SettingsCheckSwitch>
        </div>
      </VList>
      <p
        v-else
        class="m-0 py-2 text-[12px] text-muted"
      >
        Nothing matches.
      </p>
    </template>
    <p
      v-else
      class="m-0 py-2 text-[12px] text-muted"
    >
      No sessions found.
    </p>

    <div class="set-row mt-4">
      <div class="set-label">
        <span>Overwrite existing</span>
        <span class="set-desc">Replace config already on your server. Sessions are never duplicated either way.</span>
      </div>
      <HToggle
        v-model="force"
        label="Overwrite existing"
      />
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>Include secrets</span>
        <span
          class="set-desc"
          :class="secrets ? 'text-error' : ''"
        >Uploads env values in settings that look like API keys or tokens to your server. Off keeps them out.</span>
      </div>
      <HToggle
        v-model="secrets"
        label="Include secrets"
        :disabled="!settingsChosen"
      />
    </div>
    <div class="set-row items-center">
      <span class="text-[12.5px] text-muted">{{ totalLine }}</span>
      <HBtn
        size="sm"
        variant="primary"
        :disabled="!ready"
        @click="start"
      >
        Import
      </HBtn>
    </div>
  </template>
</template>
