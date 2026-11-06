<script setup lang="ts">
// Settings > Import > A folder. The same import as the offer on opening a folder, for any folder and at any time, also after
// the offer was dismissed. Scanning writes nothing; what is brought stays in the folder.
import type { RecentFolder } from '~/gen/harness/agent/v1/agent_pb'
import { isDesktop, pickFolder } from '~/lib/desktop'
import { comingOver, kindLabel, type ProjectImportItem, type ProjectImportReport, type ProjectImportScan } from '~/lib/import/project'

defineEmits<{ back: [] }>()

const store = useSessionStore()
const folders = ref<RecentFolder[]>([])
const path = ref('')
const scan = ref<ProjectImportScan | null>(null)
const report = ref<ProjectImportReport | null>(null)
const force = ref(false)
const busy = ref(false)
const error = ref('')

const rows = computed<ProjectImportItem[]>(() => report.value?.items ?? scan.value?.items ?? [])
const toBring = computed(() => comingOver(scan.value?.items ?? []).length)
const canRun = computed(() => !!scan.value && rows.value.length > 0 && (toBring.value > 0 || force.value))

const STATUS_WORD: Record<ProjectImportItem['status'], string> = { new: 'new', exists: 'already there', skipped: 'left out', imported: 'imported', error: 'failed' }

onMounted(async () => {
  try {
    folders.value = await store.transport!.listRecentFolders()
  } catch {
    // Older client: the path can still be typed.
  }
})

async function look() {
  scan.value = null
  report.value = null
  error.value = ''
  const p = path.value.trim()
  if (!p) return
  busy.value = true
  try {
    scan.value = await store.transport!.projectImport().scan(p)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function browse() {
  const p = await pickFolder(path.value)
  if (p) {
    path.value = p
    await look()
  }
}

function pick(p: string) {
  path.value = p
  void look()
}

async function run() {
  busy.value = true
  error.value = ''
  try {
    report.value = await store.transport!.projectImport().run(path.value.trim(), force.value)
    scan.value = await store.transport!.projectImport().scan(path.value.trim())
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <button
    type="button"
    class="mb-3 flex items-center gap-1.5 text-[12px] text-muted hover:text-strong"
    @click="$emit('back')"
  >
    <HIcon
      name="arrow-left"
      :size="13"
    />
    Import
  </button>
  <h2 class="set-h">
    A folder's Claude Code files
  </h2>
  <p class="set-sub">
    Brings CLAUDE.md into AGENTS.md, and what is under .claude into .harness, so that it is read. The originals stay as they are. Hooks and allow rules apply only once you trust the folder; local settings wait for that.
  </p>
  <form
    class="mb-2 flex gap-2"
    @submit.prevent="look"
  >
    <input
      v-model="path"
      placeholder="Folder path"
      aria-label="Folder path"
      class="h-8 min-w-0 flex-1 border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent"
    >
    <HBtn
      v-if="isDesktop()"
      @click="browse"
    >
      Choose…
    </HBtn>
    <HBtn
      type="submit"
      :disabled="busy || !path.trim()"
    >
      Look
    </HBtn>
  </form>
  <div
    v-if="folders.length && !scan"
    class="mb-3 flex flex-wrap gap-1.5"
  >
    <HBtn
      v-for="f in folders.slice(0, 6)"
      :key="f.path"
      :title="f.path"
      @click="pick(f.path)"
    >
      {{ f.name }}
    </HBtn>
  </div>
  <p
    v-if="error"
    role="alert"
    class="m-0 mb-3 text-[12px] text-error"
  >
    {{ error }}
  </p>
  <template v-if="scan">
    <p
      v-if="!rows.length"
      class="set-sub"
    >
      No Claude Code files in {{ scan.root }}.
    </p>
    <template v-else>
      <p class="m-0 mb-2 truncate text-[11.5px] text-muted">
        Into {{ scan.root }}
      </p>
      <div
        v-for="i in rows"
        :key="i.source"
        class="py-1 text-[12.5px]"
      >
        <div class="flex items-baseline justify-between gap-4">
          <span class="min-w-0 truncate text-strong">{{ kindLabel(i.kind) }}: {{ i.source }} → {{ i.target }}</span>
          <span
            class="shrink-0 text-[11.5px]"
            :class="i.status === 'error' ? 'text-error' : 'text-muted'"
          >{{ STATUS_WORD[i.status] }}</span>
        </div>
        <p
          v-if="i.note"
          class="m-0 text-[11.5px] text-muted"
        >
          {{ i.note }}
        </p>
      </div>
      <div class="set-row mt-4">
        <div class="set-label">
          <span>Replace what differs</span>
          <span class="set-desc">An earlier import, or a file already in .harness. Off, those stay as they are.</span>
        </div>
        <HToggle
          v-model="force"
          label="Replace what differs"
        />
      </div>
      <HBtn
        variant="primary"
        size="sm"
        class="mt-3"
        :disabled="busy || !canRun"
        @click="run"
      >
        Import
      </HBtn>
    </template>
  </template>
</template>
