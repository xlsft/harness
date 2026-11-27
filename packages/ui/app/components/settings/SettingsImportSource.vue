<script setup lang="ts">
// Settings > Import > a tool: the one screen for every source of the client's import engine. Scan (on opening, or once
// a folder is chosen), choose, run with progress and Cancel, then the report. What each item is, where it goes and
// whether it is already there comes from the client; overwriting and secrets are explicit switches.
import {
  chosenIds,
  choosable,
  defaultChoice,
  secretsMatter,
  sourceSections,
  toggleSection,
  type ImportSourceInfo,
  type SourceItem,
  type SourceScan,
  type SourceSection
} from '~/lib/import/sources'
import { IDLE_RUN, importCounts, importErrors, reduceImportRun, type ImportHandle, type ImportRunEvent } from '~/lib/import/types'

const props = defineProps<{ source: ImportSourceInfo }>()
defineEmits<{ back: [] }>()

const store = useSessionStore()
// A source with both levels starts on this computer; a folder-only one asks for the folder first.
const level = ref<'user' | 'folder'>(props.source.userLevel ? 'user' : 'folder')
const folder = ref('')
const scan = ref<SourceScan | null>(null)
const scanning = ref(false)
const error = ref('')
const picked = ref<Set<string>>(new Set())
const force = ref(false)
const secrets = ref(false)
const run = ref(IDLE_RUN)
let handle: ImportHandle | null = null

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load(path?: string) {
  scanning.value = true
  error.value = ''
  scan.value = null
  try {
    if (!store.transport) throw new Error('not connected to the client')
    scan.value = await store.transport.importSources().scan(props.source.id, path)
    picked.value = defaultChoice(scan.value.items)
  } catch (e) {
    error.value = message(e)
  }
  scanning.value = false
}

onMounted(() => {
  if (level.value === 'user') void load()
})
onBeforeUnmount(() => handle?.cancel())

function setLevel(l: 'user' | 'folder') {
  level.value = l
  scan.value = null
  error.value = ''
  if (l === 'user') void load()
}

const sections = computed(() => sourceSections(scan.value?.items ?? []))
const ids = computed(() => chosenIds(scan.value?.items ?? [], picked.value, force.value))
const anySecrets = computed(() => (scan.value?.items ?? []).some(i => i.hasSecrets))
const secretsChosen = computed(() => secretsMatter(scan.value?.items ?? [], picked.value))
const STATUS_WORD: Record<SourceItem['status'], string> = {
  new: tt('new'),
  exists: tt('already there'),
  skipped: tt('left out'),
  manual: tt('set up by hand'),
  imported: tt('imported'),
  queued: tt('waiting'),
  error: tt('failed')
}

function toggle(i: SourceItem) {
  const next = new Set(picked.value)
  if (!next.delete(i.id)) next.add(i.id)
  picked.value = next
}
function toggleAll(s: SourceSection) {
  picked.value = toggleSection(s, picked.value, force.value)
}
function allPicked(s: SourceSection) {
  const list = s.items.filter(i => choosable(i, force.value))
  return list.length > 0 && list.every(i => picked.value.has(i.id))
}

const dispatch = (e: ImportRunEvent) => (run.value = reduceImportRun(run.value, e))

function start() {
  if (!store.transport || !scan.value || !ids.value.length) return
  dispatch({ type: 'start' })
  handle = store.transport.importSources().run(
    { sourceId: props.source.id, folder: level.value === 'folder' ? folder.value.trim() : undefined, itemIds: ids.value, includeSecrets: secrets.value, force: force.value },
    { onProgress: progress => dispatch({ type: 'progress', progress }) }
  )
  handle.done
    .then(report => dispatch(report ? { type: 'report', report } : { type: 'cancelled' }))
    .catch(e => dispatch({ type: 'failed', error: message(e) }))
    .finally(() => (handle = null))
}

function again() {
  run.value = IDLE_RUN
  force.value = false
  secrets.value = false
  void load(level.value === 'folder' ? folder.value.trim() : undefined)
}

const percent = computed(() => (run.value.total ? Math.round((run.value.done / run.value.total) * 100) : 0))
const counts = computed(() => importCounts(run.value))
const errors = computed(() => importErrors(run.value))
const summary = computed(() => {
  const c = counts.value
  const parts: string[] = []
  if (c.imported) parts.push(tt('{0} imported', [c.imported]))
  if (c.exists) parts.push(tt('{0} already there', [c.exists]))
  if (c.skipped) parts.push(tt('{0} skipped', [c.skipped]))
  if (c.manual) parts.push(tt('{0} to set up by hand', [c.manual]))
  if (c.errors) parts.push(tt('{0} failed', [c.errors]))
  return parts.join(', ') || tt('Nothing was imported.')
})
</script>

<template>
  <div class="mb-1 flex items-center gap-2">
    <button
      type="button"
      :aria-label="tt('Back to import sources')"
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
      {{ tt('Import from {0}', [source.name]) }}
    </h2>
  </div>
  <p class="set-sub">
    {{ source.description }}
  </p>

  <div
    v-if="source.userLevel && source.folderLevel && run.phase === 'idle'"
    role="tablist"
    class="mb-3 flex gap-1.5"
  >
    <HBtn
      size="sm"
      role="tab"
      :aria-selected="level === 'user'"
      :variant="level === 'user' ? 'primary' : undefined"
      @click="setLevel('user')"
    >
      {{ tt('This computer') }}
    </HBtn>
    <HBtn
      size="sm"
      role="tab"
      :aria-selected="level === 'folder'"
      :variant="level === 'folder' ? 'primary' : undefined"
      @click="setLevel('folder')"
    >
      {{ tt('A folder') }}
    </HBtn>
  </div>

  <SettingsImportFolder
    v-if="level === 'folder' && run.phase === 'idle'"
    v-model="folder"
    :busy="scanning"
    :show-recent="!scan"
    @look="load"
  />

  <p
    v-if="scanning"
    class="set-sub"
  >
    {{ tt('Looking…') }}
  </p>
  <p
    v-else-if="error"
    role="alert"
    class="set-sub text-error"
  >
    {{ error }}
  </p>

  <!-- Running, and what it came to. -->
  <template v-else-if="run.phase !== 'idle'">
    <template v-if="run.phase === 'running'">
      <p class="set-sub">
        {{ tt('{0} of {1} · {2}', [run.done, run.total || '…', run.current || tt('Starting…')]) }}
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
        {{ tt('Cancel') }}
      </HBtn>
    </template>
    <template v-else>
      <p class="set-sub">
        <template v-if="run.phase === 'cancelled'">
          {{ tt('Cancelled after {0} of {1}. Importing again goes on from here.', [run.done, run.total]) }}
        </template>
        <template v-else-if="run.phase === 'failed'">
          {{ tt('Stopped: {0}', [run.error]) }}
        </template>
        <template v-else>
          {{ tt('Done.') }}
        </template>
        {{ summary }}
      </p>
      <ul
        :aria-label="tt('Import report')"
        class="m-0 mb-3 flex list-none flex-col gap-1 p-0 text-[12px]"
      >
        <li
          v-for="(i, n) in run.items"
          :key="n"
        >
          <span class="text-strong">{{ i.kind }} {{ i.name }}</span>
          <span
            class="ml-2"
            :class="i.status === 'error' ? 'text-error' : 'text-muted'"
          >{{ STATUS_WORD[i.status] }}<template v-if="i.note"> · {{ i.note }}</template></span>
        </li>
      </ul>
      <p
        v-if="errors.length"
        class="m-0 mb-3 text-[12px] text-error"
      >
        {{ tt('{0} could not be imported.', [errors.length]) }}
      </p>
      <HBtn
        size="sm"
        @click="again"
      >
        {{ tt('Back to the list') }}
      </HBtn>
    </template>
  </template>

  <!-- The choice. -->
  <template v-else-if="scan">
    <p
      v-if="!scan.items.length"
      class="set-sub"
    >
      {{ scan.root ? tt('Nothing to import in {0}.', [scan.root]) : tt('Nothing to import on this computer.') }}
    </p>
    <template v-else>
      <p
        v-if="scan.root"
        class="m-0 mb-2 truncate text-[11.5px] text-muted"
      >
        {{ tt('Into {0}', [scan.root]) }}
      </p>
      <p
        v-if="!scan.root && !scan.serverChecked && scan.connected"
        class="m-0 mb-3 text-[12px] text-muted"
      >
        {{ tt('The server did not answer, so some items marked new may already be there.') }}
      </p>
      <section
        v-for="s in sections"
        :key="s.key"
        :aria-label="s.label"
        class="mb-4"
      >
        <div class="mb-1 flex items-center gap-2">
          <h3 class="m-0 flex-1 text-[13px] font-semibold text-strong">
            {{ s.label }}
          </h3>
          <HBtn
            size="xs"
            :disabled="!s.items.some(i => choosable(i, force))"
            @click="toggleAll(s)"
          >
            {{ allPicked(s) ? tt('Clear') : tt('Select all') }}
          </HBtn>
        </div>
        <div
          v-for="i in s.items"
          :key="i.id"
          class="py-1 text-[12.5px]"
        >
          <div class="flex items-baseline justify-between gap-4">
            <SettingsCheckSwitch
              :checked="picked.has(i.id) && choosable(i, force)"
              :label="i.name"
              :disabled="!choosable(i, force)"
              class="min-w-0 flex-1"
              @toggle="toggle(i)"
            >
              <span class="flex min-w-0 flex-col text-left">
                <span class="truncate text-strong">{{ i.name }}</span>
                <span class="truncate text-[11px] text-muted">{{ i.source }}<template v-if="i.target"> → {{ i.target }}</template></span>
              </span>
            </SettingsCheckSwitch>
            <span
              class="shrink-0 text-[11.5px]"
              :class="i.status === 'error' ? 'text-error' : 'text-muted'"
            >{{ STATUS_WORD[i.status] }}</span>
          </div>
          <p
            v-if="i.note"
            class="m-0 pl-6 text-[11.5px] text-muted"
          >
            {{ i.note }}
          </p>
        </div>
      </section>

      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Overwrite existing') }}</span>
          <span class="set-desc">{{ tt('Replace what is already there with the same name. Off, it stays as it is.') }}</span>
        </div>
        <HToggle
          v-model="force"
          :label="tt('Overwrite existing')"
        />
      </div>
      <div
        v-if="anySecrets"
        class="set-row"
      >
        <div class="set-label">
          <span>{{ tt('Include secrets') }}</span>
          <span
            class="set-desc"
            :class="secrets ? 'text-error' : ''"
          >{{ tt('Brings values that look like API keys or tokens. Off leaves them empty, to fill in later.') }}</span>
        </div>
        <HToggle
          v-model="secrets"
          :label="tt('Include secrets')"
          :disabled="!secretsChosen"
        />
      </div>
      <div class="set-row items-center">
        <span class="text-[12.5px] text-muted">{{ ids.length ? tt('{0} chosen', [ids.length]) : tt('Nothing chosen yet') }}</span>
        <HBtn
          size="sm"
          variant="primary"
          :disabled="!ids.length"
          @click="start"
        >
          {{ tt('Import') }}
        </HBtn>
      </div>
    </template>
  </template>
</template>
