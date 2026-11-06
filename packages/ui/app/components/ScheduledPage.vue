<script setup lang="ts">
// Scheduled: list, create, edit, pause, run now and history. The client decides what is due and what is allowed; this
// shows what it reports and sends what the user chose. The list is asked again every few seconds, as runs start by
// themselves.
import type { RecentFolder } from '~/gen/harness/agent/v1/agent_pb'
import {
  emptyScheduleDraft,
  formatWhen,
  machineText,
  originText,
  runSourceText,
  runStatusText,
  runStatusTone,
  scheduleDraftOf,
  scheduleOfDraft,
  ScheduleTriggerKind,
  sortSchedules,
  stateText,
  TRIGGER_OPTIONS,
  triggerText,
  type Schedule,
  type ScheduleDraft,
  type ScheduleList,
  type ScheduleRun
} from '~/lib/schedule/types'

const store = useSessionStore()
const api = computed(() => store.transport?.schedules())
const data = ref<ScheduleList | null>(null)
const error = ref('')
const notice = ref('')
const folders = ref<RecentFolder[]>([])
const draft = ref<ScheduleDraft | null>(null)
const saving = ref(false)
const openRuns = ref('')
const runs = ref<ScheduleRun[]>([])

const list = computed(() => sortSchedules(data.value?.schedules ?? []))

const folderOptions = computed(() => {
  const paths = new Set(folders.value.map(f => f.path))
  if (draft.value?.cwd) paths.add(draft.value.cwd)
  return [{ value: '', label: 'No folder' }, ...[...paths].map(p => ({ value: p, label: p }))]
})

const modeOptions = [
  { value: '', label: 'As in settings' },
  { value: 'default', label: 'Ask for everything' },
  { value: 'acceptEdits', label: 'Accept edits' },
  { value: 'auto', label: 'Auto' },
  { value: 'plan', label: 'Plan only' },
  { value: 'dontAsk', label: 'Refuse what would ask' },
  { value: 'bypassPermissions', label: 'Skip permission checks' }
]

const message = (err: unknown) => (err instanceof Error ? err.message : String(err))

async function load() {
  if (!api.value) return
  try {
    data.value = await api.value.list()
    error.value = ''
    if (openRuns.value) runs.value = await api.value.runs(openRuns.value)
  } catch (err) {
    error.value = message(err)
  }
}

// What may run a schedule while its machine is away: an always-on host, with a checkout of its own.
const hostChoices = ref<{ value: string, label: string }[]>([])

let timer: ReturnType<typeof setInterval> | undefined
onMounted(async () => {
  await load()
  timer = setInterval(load, 5000)
  try {
    const hosts = (await store.transport?.hosts().list()) ?? []
    hostChoices.value = [{ value: '', label: 'Wait for it' }, ...hosts.map(h => ({ value: h.id, label: `Run on ${h.name}` }))]
  } catch { /* no hosts to offer */ }
  try {
    folders.value = (await store.transport?.listRecentFolders()) ?? []
  } catch { /* a folder can still be kept as it is */ }
})
onUnmounted(() => clearInterval(timer))

async function act(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
    await load()
  } catch (err) {
    error.value = message(err)
  }
}

function startNew() {
  draft.value = emptyScheduleDraft(store.current?.meta.noFolder ? '' : store.current?.meta.cwd ?? '')
}

function edit(s: Schedule) {
  draft.value = scheduleDraftOf(s)
}

async function save() {
  const d = draft.value
  if (!d || !api.value) return
  saving.value = true
  try {
    const out = scheduleOfDraft(d)
    await (d.id ? api.value.update(out) : api.value.create(out))
    draft.value = null
    error.value = ''
    await load()
  } catch (err) {
    error.value = message(err)
  } finally {
    saving.value = false
  }
}

async function runNow(s: Schedule) {
  await act(async () => {
    const res = await api.value?.runNow(s.id)
    notice.value = res?.queued ? `Asked ${s.deviceName || 'its machine'} to run it. It starts when that machine is up.` : ''
  })
}

async function toggleRuns(s: Schedule) {
  if (openRuns.value === s.id) {
    openRuns.value = ''
    return
  }
  openRuns.value = s.id
  runs.value = []
  await act(async () => {
    runs.value = (await api.value?.runs(s.id)) ?? []
  })
}

function openSession(id: string) {
  store.open(id)
  navigateTo({ path: '/' })
}

async function setEnabled(on: boolean) {
  await act(() => store.transport!.updateUserPreferences({ schedulesEnabled: on }))
}

const isWebhook = (s: Schedule) => s.trigger?.kind === ScheduleTriggerKind.WEBHOOK
</script>

<template>
  <section
    aria-label="Scheduled"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[860px] flex-col gap-[22px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <div class="flex items-center gap-3">
        <h1 class="m-0 flex-1 font-display text-[26px] font-bold text-strong">
          Scheduled
        </h1>
        <div
          v-if="data"
          class="flex items-center gap-2 text-[12px] text-muted"
        >
          <span>Run on their own</span>
          <HToggle
            :model-value="data.enabled"
            label="Run schedules on their own"
            @update:model-value="setEnabled"
          />
        </div>
        <HBtn
          variant="primary"
          size="md"
          @click="startNew"
        >
          New schedule
        </HBtn>
      </div>

      <p class="m-0 text-[12.5px] text-muted">
        Tasks run on a machine where xlsft`s harness is up: what comes due while it's down runs when it's back, or is recorded as missed.
        <template v-if="data && !data.synced">
          No server is connected, so these stay on this machine.
        </template>
      </p>

      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>
      <p
        v-if="notice"
        role="status"
        class="m-0 border border-l-2 border-default border-l-accent px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ notice }}
      </p>

      <form
        v-if="draft"
        aria-label="Schedule"
        class="flex flex-col gap-3 border border-default p-4 text-[12.5px]"
        @submit.prevent="save"
      >
        <label class="flex flex-col gap-1 text-muted">
          Name
          <input
            v-model="draft.name"
            class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
            placeholder="Optional"
          >
        </label>
        <label class="flex flex-col gap-1 text-muted">
          Prompt
          <textarea
            v-model="draft.prompt"
            rows="4"
            class="resize-y border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
            placeholder="What to do. A slash command works too."
          />
        </label>
        <div class="flex flex-wrap gap-3">
          <div class="flex min-w-[220px] flex-1 flex-col gap-1 text-muted">
            When
            <HSelect
              v-model="draft.trigger"
              :options="TRIGGER_OPTIONS"
              aria-label="When"
            />
          </div>
          <label
            v-if="draft.trigger === 'once'"
            class="flex min-w-[220px] flex-1 flex-col gap-1 text-muted"
          >
            At
            <input
              v-model="draft.at"
              type="datetime-local"
              class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
            >
          </label>
          <label
            v-if="draft.trigger === 'interval'"
            class="flex min-w-[220px] flex-1 flex-col gap-1 text-muted"
          >
            Minutes between runs
            <input
              v-model="draft.everyMinutes"
              inputmode="decimal"
              class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
            >
          </label>
          <label
            v-if="draft.trigger === 'cron'"
            class="flex min-w-[220px] flex-1 flex-col gap-1 text-muted"
          >
            Minute, hour, day, month, weekday
            <input
              v-model="draft.cron"
              class="border border-default bg-transparent px-2 py-1 font-mono text-strong outline-none focus:border-accent"
            >
          </label>
        </div>
        <label
          v-if="draft.trigger === 'file'"
          class="flex flex-col gap-1 text-muted"
        >
          Files to watch in the folder, one pattern per line
          <textarea
            v-model="draft.paths"
            rows="2"
            class="resize-y border border-default bg-transparent px-2 py-1 font-mono text-strong outline-none focus:border-accent"
            placeholder="src/**/*.go"
          />
        </label>
        <p
          v-if="draft.trigger === 'webhook'"
          class="m-0 text-muted"
        >
          The address to post to appears here once the schedule has reached the server. What is posted reaches the run as data, never as instructions.
        </p>
        <div class="flex flex-wrap gap-3">
          <div class="flex min-w-[220px] flex-1 flex-col gap-1 text-muted">
            Folder
            <HSelect
              v-model="draft.cwd"
              :options="folderOptions"
              aria-label="Folder"
            />
          </div>
          <div class="flex min-w-[220px] flex-1 flex-col gap-1 text-muted">
            Permissions
            <HSelect
              v-model="draft.permissionMode"
              :options="modeOptions"
              aria-label="Permissions"
            />
          </div>
        </div>
        <div class="flex flex-wrap items-center gap-5 text-muted">
          <span class="flex items-center gap-2">
            <HToggle
              v-model="draft.catchUp"
              label="Run once when back if a time was missed"
            />
            Run once when back if a time was missed
          </span>
          <span class="flex items-center gap-2">
            <HToggle
              v-model="draft.worktree"
              label="Each run in a new git worktree"
            />
            Each run in a new git worktree
          </span>
        </div>
        <div
          v-if="hostChoices.length > 1"
          class="flex flex-wrap items-center gap-3 text-muted"
        >
          <span class="flex items-center gap-2">
            While its machine is away
            <HSelect
              v-model="draft.awayHostId"
              :options="hostChoices"
              aria-label="While its machine is away"
            />
          </span>
          <input
            v-if="draft.awayHostId"
            v-model="draft.repositoryKey"
            placeholder="host/owner/repository (default: the folder's remote)"
            class="h-8 min-w-[280px] flex-1 border border-line bg-transparent px-2 text-strong outline-none focus:border-accent"
          >
        </div>
        <div class="flex gap-2">
          <HBtn
            variant="primary"
            size="md"
            type="submit"
            :disabled="saving"
          >
            {{ draft.id ? 'Save' : 'Create' }}
          </HBtn>
          <HBtn
            size="md"
            @click="draft = null"
          >
            Cancel
          </HBtn>
        </div>
      </form>

      <p
        v-if="data && !list.length && !draft"
        class="m-0 text-[12.5px] text-muted"
      >
        Nothing is scheduled. Make a task here, or type /loop 10m &lt;prompt&gt; in a session.
      </p>

      <div class="flex flex-col">
        <div
          v-for="s in list"
          :key="s.id"
          data-schedule
          class="flex flex-col gap-1.5 border-t border-default py-3 text-[12.5px]"
          :class="(s.paused || s.done) && 'opacity-70'"
        >
          <div class="flex flex-wrap items-baseline gap-2">
            <span class="text-strong">{{ s.name || s.prompt.slice(0, 60) }}</span>
            <span
              v-if="originText(s)"
              class="border border-default px-1 text-[10px] leading-[14px] text-muted"
            >{{ originText(s) }}</span>
            <span class="border border-default px-1 text-[10px] leading-[14px] text-muted">{{ machineText(s) }}</span>
            <span class="text-muted">{{ stateText(s) }}</span>
          </div>
          <span class="text-toned">{{ triggerText(s) }}<template v-if="s.cwd"> · {{ s.cwd }}</template></span>
          <span
            v-if="s.lastRun"
            class="text-[11.5px] text-muted"
          >Last run {{ formatWhen(s.lastRun.startedAt) }}: <span :class="runStatusTone(s.lastRun)">{{ runStatusText(s.lastRun) }}</span></span>
          <span
            v-if="s.problem"
            class="text-[11.5px] text-warn"
          >{{ s.problem }}</span>
          <div
            v-if="isWebhook(s) && s.trigger?.webhookUrl"
            class="flex items-center gap-2 text-[11.5px] text-muted"
          >
            <code class="min-w-0 flex-1 truncate">{{ s.trigger.webhookUrl }}</code>
            <HBtn @click="act(() => api!.rotateWebhook(s.id))">
              New address
            </HBtn>
          </div>
          <div class="flex flex-wrap gap-1.5 pt-1">
            <HBtn
              v-if="s.needsTrust"
              variant="primary"
              @click="act(() => api!.trust(s.id))"
            >
              Allow on this machine
            </HBtn>
            <HBtn @click="runNow(s)">
              Run now
            </HBtn>
            <HBtn
              v-if="!s.done"
              @click="act(() => api!.setPaused(s.id, !s.paused))"
            >
              {{ s.paused ? 'Resume' : 'Pause' }}
            </HBtn>
            <HBtn
              v-if="!s.sessionId"
              @click="edit(s)"
            >
              Edit
            </HBtn>
            <HBtn
              v-if="!s.thisDevice && data?.deviceId && !s.deviceLocal"
              @click="act(() => api!.update({ ...s, deviceId: data!.deviceId }))"
            >
              Run here instead
            </HBtn>
            <HBtn @click="toggleRuns(s)">
              History
            </HBtn>
            <HBtn
              tone="danger"
              @click="act(() => api!.remove(s.id))"
            >
              Delete
            </HBtn>
          </div>
          <div
            v-if="openRuns === s.id"
            class="mt-1 flex flex-col border-l border-default pl-3"
          >
            <span
              v-if="!runs.length"
              class="py-1 text-[11.5px] text-muted"
            >It hasn't run yet.</span>
            <div
              v-for="r in runs"
              :key="r.id"
              class="flex flex-col gap-0.5 py-1 text-[11.5px]"
            >
              <div class="flex flex-wrap items-baseline gap-2">
                <span :class="runStatusTone(r)">{{ runStatusText(r) }}</span>
                <span class="text-muted">{{ formatWhen(r.startedAt) }} · {{ runSourceText(r) }}<template v-if="r.deviceName"> · {{ r.deviceName }}</template></span>
                <HBtn
                  v-if="r.sessionId"
                  variant="ghost"
                  @click="openSession(r.sessionId)"
                >
                  Open session
                </HBtn>
              </div>
              <span
                v-if="r.summary"
                class="text-toned"
              >{{ r.summary }}</span>
              <span
                v-if="r.error"
                class="text-bad"
              >{{ r.error }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
