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
  cronText,
  sortSchedules,
  stateText,
  triggerText,
  type Schedule,
  type ScheduleDraft,
  type ScheduleList,
  type ScheduleRun
} from '~/lib/schedule/types'
import { draftOfTemplate, SCHEDULE_TEMPLATES, type ScheduleTemplate } from '~/lib/schedule/templates'

const store = useSessionStore()
const api = computed(() => store.transport?.schedules())
const data = ref<ScheduleList | null>(null)
const error = ref('')
const notice = ref('')
const folders = ref<RecentFolder[]>([])
const draft = ref<ScheduleDraft | null>(null)
const tab = ref<'yours' | 'templates'>('yours')
const TABS = [{ value: 'yours', label: tt('Yours') }, { value: 'templates', label: tt('Templates') }] as const
const openRuns = ref('')
const runs = ref<ScheduleRun[]>([])

const list = computed(() => sortSchedules(data.value?.schedules ?? []))

const folderOptions = computed(() => {
  const paths = new Set(folders.value.map(f => f.path))
  if (draft.value?.cwd) paths.add(draft.value.cwd)
  return [{ value: '', label: tt('No folder') }, ...[...paths].map(p => ({ value: p, label: p }))]
})

const modeOptions = [
  { value: '', label: tt('As in settings') },
  { value: 'default', label: tt('Ask for everything') },
  { value: 'acceptEdits', label: tt('Accept edits') },
  { value: 'auto', label: tt('Auto') },
  { value: 'plan', label: tt('Plan only') },
  { value: 'dontAsk', label: tt('Refuse what would ask') },
  { value: 'bypassPermissions', label: tt('Skip permission checks') }
]

const message = (err: unknown) => (err instanceof Error ? err.message : String(err))

let turnedOn = false

async function load() {
  if (!api.value) return
  try {
    data.value = await api.value.list()
    error.value = ''
    // Schedules always run on their own; an older switch left off is turned back on once.
    if (!data.value.enabled && !turnedOn) {
      turnedOn = true
      await store.transport?.updateUserPreferences({ schedulesEnabled: true })
    }
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
    hostChoices.value = [{ value: '', label: tt('Wait for it') }, ...hosts.map(h => ({ value: h.id, label: tt('Run on {0}', [h.name]) }))]
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

const currentFolder = () => (store.current?.meta.noFolder ? '' : store.current?.meta.cwd ?? '')

function startNew() {
  draft.value = emptyScheduleDraft(currentFolder())
}

function fromTemplate(t: ScheduleTemplate) {
  draft.value = draftOfTemplate(t, currentFolder())
}

function edit(s: Schedule) {
  saveError.value = ''
  draft.value = scheduleDraftOf(s)
}

// The dialog's save: true closes it; a refusal shows on the page under the dialog's backdrop and in the dialog.
const saveError = ref('')
async function save(d: ScheduleDraft): Promise<boolean> {
  if (!api.value) return false
  try {
    const out = scheduleOfDraft(d)
    await (d.id ? api.value.update(out) : api.value.create(out))
    saveError.value = ''
    tab.value = 'yours'
    await load()
    return true
  } catch (err) {
    saveError.value = message(err)
    return false
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

const isWebhook = (s: Schedule) => s.trigger?.kind === ScheduleTriggerKind.WEBHOOK
</script>

<template>
  <PageShell :title="tt('Scheduled tasks')">
    <div class="flex flex-wrap items-center gap-2">
      <PageTabs
        v-model="tab"
        :tabs="TABS"
      />
      <span class="flex-1" />
      <HBtn
        variant="primary"
        size="md"
        @click="startNew"
      >
        <HIcon
          name="plus"
          :size="12"
        />{{ tt('New task') }}
      </HBtn>
    </div>

    <p class="m-0 text-[12.5px] text-muted">
      {{ tt('Tasks run on a machine where xlsft`s harness is up: what comes due while it\'s down runs when it\'s back, or is recorded as missed.') }}
      <template v-if="data && !data.synced">
        {{ tt('No server is connected, so these stay on this machine.') }}
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

    <template v-if="tab === 'yours'">
      <!-- Nothing yet: a way to start from scratch, and the templates right under it. -->
      <div
        v-if="data && !list.length"
        class="flex flex-col items-center gap-3 py-10 text-center"
      >
        <span class="flex size-14 items-center justify-center rounded-full border border-line text-muted">
          <HIcon
            name="clock"
            :size="26"
          />
        </span>
        <p class="m-0 text-[13px] text-muted">
          {{ tt('No scheduled tasks yet.') }}
        </p>
        <p class="m-0 max-w-[420px] text-[12px] text-dimmed">
          {{ tt('Start from a template below, set one up by hand, or type /loop 10m <prompt> in a session.') }}
        </p>
        <HBtn
          size="md"
          @click="startNew"
        >
          {{ tt('Set up manually') }}
        </HBtn>
      </div>

      <div
        v-else-if="list.length"
        class="overflow-hidden border border-default"
      >
        <div
          v-for="(s, i) in list"
          :key="s.id"
          data-schedule
          class="flex flex-col"
          :class="{ 'border-t border-default': i > 0 }"
        >
          <div
            class="flex items-start gap-3 px-3 py-2.5"
            :class="(s.paused || s.done) && 'opacity-70'"
          >
            <SettingsMonogram :icon="isWebhook(s) ? 'hook' : 'clock'" />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="flex flex-wrap items-center gap-1.5 text-[12.5px] text-strong">
                <span class="truncate">{{ s.name || s.prompt.slice(0, 60) }}</span>
                <span
                  v-if="originText(s)"
                  class="border border-default px-1 text-[10px] leading-[14px] text-muted"
                >{{ originText(s) }}</span>
                <span class="text-[11px] text-muted">· {{ stateText(s) }}</span>
              </span>
              <span class="flex items-center gap-1.5 truncate text-[11.5px] text-muted">
                <HIcon
                  name="clock"
                  :size="11"
                />{{ triggerText(s) }}<template v-if="s.cwd"> · {{ s.cwd }}</template>
              </span>
              <span class="text-[11px] text-dimmed">
                {{ machineText(s) }}<template v-if="s.lastRun"> · {{ tt('last run {0}:', [formatWhen(s.lastRun.startedAt)]) }} <span :class="runStatusTone(s.lastRun)">{{ runStatusText(s.lastRun) }}</span></template>
              </span>
              <span
                v-if="s.problem"
                class="text-[11.5px] text-warn"
              >{{ s.problem }}</span>
            </span>
            <div class="flex shrink-0 items-center gap-0.5">
              <HBtn
                v-if="s.needsTrust"
                variant="primary"
                @click="act(() => api!.trust(s.id))"
              >
                {{ tt('Allow on this machine') }}
              </HBtn>
              <HIconBtn
                icon="play"
                :size="26"
                :label="tt('Run now')"
                :tooltip="tt('Run now')"
                @click="runNow(s)"
              />
              <HIconBtn
                v-if="!s.done"
                :icon="s.paused ? 'reload' : 'stop'"
                :size="26"
                :label="s.paused ? tt('Resume') : tt('Pause')"
                :tooltip="s.paused ? tt('Resume') : tt('Pause')"
                @click="act(() => api!.setPaused(s.id, !s.paused))"
              />
              <HIconBtn
                v-if="!s.sessionId"
                icon="edit"
                :size="26"
                :label="tt('Edit')"
                :tooltip="tt('Edit')"
                @click="edit(s)"
              />
              <HIconBtn
                icon="history"
                :size="26"
                :label="tt('History')"
                :tooltip="tt('History')"
                :pressed="openRuns === s.id"
                @click="toggleRuns(s)"
              />
              <HIconBtn
                icon="trash"
                :size="26"
                :label="tt('Delete')"
                :tooltip="tt('Delete')"
                @click="act(() => api!.remove(s.id))"
              />
            </div>
          </div>
          <div
            v-if="isWebhook(s) && s.trigger?.webhookUrl"
            class="flex items-center gap-2 pr-3 pb-2.5 pl-[52px] text-[11.5px] text-muted"
          >
            <code class="min-w-0 flex-1 truncate">{{ s.trigger.webhookUrl }}</code>
            <HBtn @click="act(() => api!.rotateWebhook(s.id))">
              {{ tt('New address') }}
            </HBtn>
          </div>
          <div
            v-if="!s.thisDevice && data?.deviceId && !s.deviceLocal"
            class="pr-3 pb-2.5 pl-[52px]"
          >
            <HBtn @click="act(() => api!.update({ ...s, deviceId: data!.deviceId }))">
              {{ tt('Run here instead') }}
            </HBtn>
          </div>
          <div
            v-if="openRuns === s.id"
            class="flex flex-col pr-3 pb-2.5 pl-[52px]"
          >
            <span
              v-if="!runs.length"
              class="py-1 text-[11.5px] text-muted"
            >{{ tt('It hasn\'t run yet.') }}</span>
            <div
              v-for="r in runs"
              :key="r.id"
              class="flex flex-col gap-0.5 border-t border-default py-1.5 text-[11.5px] first:border-t-0"
            >
              <div class="flex flex-wrap items-baseline gap-2">
                <span :class="runStatusTone(r)">{{ runStatusText(r) }}</span>
                <span class="text-muted">{{ formatWhen(r.startedAt) }} · {{ runSourceText(r) }}<template v-if="r.deviceName"> · {{ r.deviceName }}</template></span>
                <HBtn
                  v-if="r.sessionId"
                  variant="ghost"
                  @click="openSession(r.sessionId)"
                >
                  {{ tt('Open session') }}
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
    </template>

    <template v-if="tab === 'templates' || (data && !list.length)">
      <div class="grid grid-cols-2 gap-x-8 gap-y-6 max-sm:grid-cols-1">
        <button
          v-for="t in SCHEDULE_TEMPLATES"
          :key="t.id"
          type="button"
          class="-m-2 flex items-start gap-3 rounded-control p-2 text-left hover:bg-hover"
          @click="fromTemplate(t)"
        >
          <span class="flex size-9 shrink-0 items-center justify-center rounded-control border border-line bg-raised text-toned">
            <HIcon
              :name="t.icon"
              :size="16"
            />
          </span>
          <span class="flex min-w-0 flex-col gap-1">
            <span class="text-[13px] text-strong">{{ t.name }}</span>
            <span class="text-[12px] leading-[1.45] text-muted">{{ t.about }}</span>
            <span class="flex items-center gap-1.5 text-[11.5px] text-dimmed">
              <HIcon
                name="clock"
                :size="11"
              />{{ cronText(t.cron) }}
            </span>
          </span>
        </button>
      </div>
    </template>

    <ScheduleModal
      v-if="draft"
      :draft="draft"
      :folders="folderOptions"
      :modes="modeOptions"
      :hosts="hostChoices"
      :save="save"
      :error="saveError"
      @close="draft = null"
    />
  </PageShell>
</template>
