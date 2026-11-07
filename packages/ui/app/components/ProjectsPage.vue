<script setup lang="ts">
// Projects: a goal handed to a coordinator session, which splits it into tasks and starts worker sessions. The client
// runs it, enforces the limits and keeps it across restarts (docs/design/coordinator.md); this shows the runs, starts
// one, and pauses, resumes, stops or limits it. The list is asked again every few seconds while the page is open.
import type { RecentFolder } from '~/gen/harness/agent/v1/agent_pb'
import { TaskNodeStatus } from '~/gen/harness/agent/v1/teamwork_pb'
import {
  activeWorkers,
  budgetShare,
  budgetText,
  ControlProjectRunRequest_Action,
  ProjectRunState,
  runEnded,
  runStateText,
  workerStateText,
  type ProjectRun,
  type ProjectWorker
} from '~/lib/inbox/types'

const store = useSessionStore()
const api = computed(() => store.transport?.projectRuns())
const runs = ref<ProjectRun[]>([])
const error = ref('')
const folders = ref<RecentFolder[]>([])
const draft = ref<{ goal: string, cwd: string, workers: string, budget: string, mode: string } | null>(null)
const saving = ref(false)
const openRun = ref('')
const limitsDraft = ref<{ workers: string, budget: string } | null>(null)

const message = (err: unknown) => (err instanceof Error ? err.message : String(err))

async function load() {
  if (!api.value) return
  try {
    runs.value = await api.value.list(true)
    error.value = ''
  } catch (err) {
    error.value = message(err)
  }
}

let timer: ReturnType<typeof setInterval> | undefined
onMounted(async () => {
  await load()
  timer = setInterval(load, 4000)
  try {
    folders.value = (await store.transport?.listRecentFolders()) ?? []
  } catch { /* a folder can still be typed */ }
})
onUnmounted(() => clearInterval(timer))

const folderOptions = computed(() => {
  const paths = new Set(folders.value.map(f => f.path))
  if (draft.value?.cwd) paths.add(draft.value.cwd)
  return [...paths].map(p => ({ value: p, label: p }))
})

const modeOptions = [
  { value: '', label: tt('As in settings') },
  { value: 'default', label: tt('Ask for everything') },
  { value: 'acceptEdits', label: tt('Accept edits') },
  { value: 'auto', label: tt('Auto') },
  { value: 'plan', label: tt('Plan only') }
]

function startNew() {
  draft.value = { goal: '', cwd: store.current?.meta.noFolder ? '' : store.current?.meta.cwd ?? folders.value[0]?.path ?? '', workers: '', budget: '', mode: '' }
}

async function create() {
  const d = draft.value
  if (!d || !api.value) return
  saving.value = true
  try {
    const run = await api.value.create({ goal: d.goal, cwd: d.cwd, permissionMode: d.mode, limits: { maxWorkers: Number(d.workers) || 0, budgetUsd: Number(d.budget) || 0 } })
    draft.value = null
    openRun.value = run.id
    await load()
  } catch (err) {
    error.value = message(err)
  } finally {
    saving.value = false
  }
}

async function control(run: ProjectRun, action: ControlProjectRunRequest_Action, limits?: { maxWorkers: number, budgetUsd: number }) {
  try {
    await api.value!.control(run.id, action, limits, run.coordinatorSessionId)
    error.value = ''
    await load()
  } catch (err) {
    error.value = message(err)
  }
}

async function remove(run: ProjectRun) {
  try {
    await api.value!.remove(run.id)
    await load()
  } catch (err) {
    error.value = message(err)
  }
}

function editLimits(run: ProjectRun) {
  limitsDraft.value = { workers: String(run.limits?.maxWorkers ?? ''), budget: run.limits?.budgetUsd ? String(run.limits.budgetUsd) : '' }
}

async function saveLimits(run: ProjectRun) {
  const l = limitsDraft.value
  if (!l) return
  await control(run, ControlProjectRunRequest_Action.SET_LIMITS, { maxWorkers: Number(l.workers) || 0, budgetUsd: Number(l.budget) || 0 })
  limitsDraft.value = null
}

function openSession(id: string) {
  store.open(id)
  navigateTo({ path: '/' })
}

const taskMark = (s: TaskNodeStatus) => s === TaskNodeStatus.DONE ? '✓' : s === TaskNodeStatus.IN_PROGRESS ? '•' : s === TaskNodeStatus.FAILED ? '×' : '○'
const workerTone = (w: ProjectWorker) => w.report ? 'text-toned' : 'text-muted'
</script>

<template>
  <section
    :aria-label="tt('Projects')"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[920px] flex-col gap-[22px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <div class="flex items-center gap-3">
        <h1 class="m-0 flex-1 font-display text-[26px] font-bold text-strong">
          {{ tt('Projects') }}
        </h1>
        <HBtn
          variant="primary"
          size="md"
          @click="startNew"
        >
          {{ tt('New project run') }}
        </HBtn>
      </div>
      <p class="m-0 text-[12.5px] text-muted">
        {{ tt('Give a goal: a coordinator session splits it into tasks and hands them to worker sessions in worktrees or on your always-on hosts. Workers never get more permissions than the coordinator, and the limits are kept for you.') }}
      </p>
      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>

      <form
        v-if="draft"
        :aria-label="tt('New project run')"
        class="flex flex-col gap-3 border border-default p-4 text-[12.5px]"
        @submit.prevent="create"
      >
        <label class="flex flex-col gap-1 text-muted">
          {{ tt('Goal') }}
          <textarea
            v-model="draft.goal"
            rows="4"
            class="resize-y border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
            :placeholder="tt('What should be done, and what done means.')"
          />
        </label>
        <div class="flex flex-wrap gap-3">
          <div class="flex min-w-[260px] flex-1 flex-col gap-1 text-muted">
            {{ tt('Folder') }}
            <HSelect
              v-if="folderOptions.length"
              v-model="draft.cwd"
              :options="folderOptions"
              :aria-label="tt('Folder')"
            />
            <input
              v-else
              v-model="draft.cwd"
              class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
              :placeholder="tt('Absolute path of the repository')"
            >
          </div>
          <div class="flex min-w-[200px] flex-1 flex-col gap-1 text-muted">
            {{ tt('Permissions') }}
            <HSelect
              v-model="draft.mode"
              :options="modeOptions"
              :aria-label="tt('Permissions')"
            />
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <label class="flex w-[180px] flex-col gap-1 text-muted">
            {{ tt('Workers at once') }}
            <input
              v-model="draft.workers"
              inputmode="numeric"
              class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
              :placeholder="tt('As in settings')"
            >
          </label>
          <label class="flex w-[180px] flex-col gap-1 text-muted">
            {{ tt('Budget, USD') }}
            <input
              v-model="draft.budget"
              inputmode="decimal"
              class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
              :placeholder="tt('None')"
            >
          </label>
        </div>
        <div class="flex gap-2">
          <HBtn
            variant="primary"
            size="md"
            type="submit"
            :disabled="saving || !draft.goal.trim() || !draft.cwd"
          >
            {{ tt('Start') }}
          </HBtn>
          <HBtn
            size="md"
            @click="draft = null"
          >
            {{ tt('Cancel') }}
          </HBtn>
        </div>
      </form>

      <p
        v-if="!runs.length && !draft"
        class="m-0 text-[12.5px] text-muted"
      >
        {{ tt('No project runs yet.') }}
      </p>

      <article
        v-for="run in runs"
        :key="run.id"
        :aria-label="run.title"
        class="flex flex-col gap-2 border border-default p-4 text-[12.5px]"
      >
        <div class="flex items-center gap-2">
          <button
            class="min-w-0 flex-1 truncate text-left text-[14px] font-semibold text-strong hover:underline"
            @click="openRun = openRun === run.id ? '' : run.id"
          >
            {{ run.title }}
          </button>
          <span
            class="border px-1.5 text-[11px]"
            :class="run.state === ProjectRunState.WAITING_FOR_USER || run.state === ProjectRunState.PAUSED ? 'border-accent text-accent' : run.state === ProjectRunState.FAILED ? 'border-bad text-bad' : 'border-line text-muted'"
          >{{ runStateText(run.state) }}</span>
        </div>
        <p
          v-if="run.reason"
          class="m-0 text-toned"
        >
          {{ run.reason }}
        </p>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11.5px] text-muted">
          <span>{{ tt('{0} of {1} workers running', [activeWorkers(run), run.limits?.maxWorkers ?? 0]) }}</span>
          <span>{{ budgetText(run) }}</span>
          <span v-if="run.deviceName">{{ run.deviceName }}</span>
          <span class="font-mono">{{ run.cwd }}</span>
        </div>
        <div
          v-if="budgetShare(run) !== undefined"
          class="h-1 w-full overflow-hidden bg-raised"
          role="progressbar"
          :aria-label="tt('Budget used')"
          :aria-valuenow="Math.round((budgetShare(run) ?? 0) * 100)"
        >
          <div
            class="h-1"
            :class="(budgetShare(run) ?? 0) >= 1 ? 'bg-bad' : 'bg-accent'"
            :style="{ width: `${Math.round((budgetShare(run) ?? 0) * 100)}%` }"
          />
        </div>

        <div
          v-if="openRun === run.id"
          class="flex flex-col gap-3 pt-1"
        >
          <p class="m-0 whitespace-pre-wrap text-toned">
            {{ run.goal }}
          </p>
          <div v-if="run.tasks.length">
            <h3 class="m-0 mb-1 text-[12px] font-semibold text-strong">
              {{ tt('Tasks') }}
            </h3>
            <ul class="m-0 flex list-none flex-col gap-0.5 p-0">
              <li
                v-for="t in run.tasks"
                :key="t.id"
                class="flex gap-2"
              >
                <span
                  class="w-3 text-muted"
                  aria-hidden="true"
                >{{ taskMark(t.status) }}</span>
                <span class="text-muted">{{ t.id }}</span>
                <span class="min-w-0 flex-1 truncate text-strong">{{ t.title }}</span>
                <span
                  v-if="t.ownerName || t.owner"
                  class="text-muted"
                >{{ t.ownerName || t.owner }}</span>
              </li>
            </ul>
          </div>
          <div>
            <h3 class="m-0 mb-1 text-[12px] font-semibold text-strong">
              {{ tt('Workers') }}
            </h3>
            <p
              v-if="!run.workers.length"
              class="m-0 text-muted"
            >
              {{ tt('None yet.') }}
            </p>
            <div
              v-for="w in run.workers"
              :key="w.name"
              class="flex flex-col gap-0.5 border-t border-line py-1.5"
            >
              <div class="flex items-center gap-2">
                <button
                  class="min-w-0 flex-1 truncate text-left text-strong hover:underline disabled:no-underline"
                  :disabled="!w.sessionId"
                  @click="openSession(w.sessionId)"
                >
                  {{ w.taskId }} · {{ w.title }}
                </button>
                <span class="text-[11.5px] text-muted">{{ w.deviceName || w.target }}</span>
                <span class="text-[11.5px] text-muted">{{ workerStateText(w.state) }}</span>
              </div>
              <p
                v-if="w.report || w.branch"
                class="m-0 line-clamp-2"
                :class="workerTone(w)"
              >
                <span
                  v-if="w.branch"
                  class="font-mono text-muted"
                >{{ w.branch }} </span>{{ w.report }}
              </p>
            </div>
          </div>
          <form
            v-if="limitsDraft"
            class="flex flex-wrap items-end gap-3"
            @submit.prevent="saveLimits(run)"
          >
            <label class="flex w-[150px] flex-col gap-1 text-muted">
              {{ tt('Workers at once') }}
              <input
                v-model="limitsDraft.workers"
                inputmode="numeric"
                class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
              >
            </label>
            <label class="flex w-[150px] flex-col gap-1 text-muted">
              {{ tt('Budget, USD') }}
              <input
                v-model="limitsDraft.budget"
                inputmode="decimal"
                class="border border-default bg-transparent px-2 py-1 text-strong outline-none focus:border-accent"
                :placeholder="tt('None')"
              >
            </label>
            <HBtn
              variant="primary"
              size="sm"
              type="submit"
            >
              {{ tt('Save') }}
            </HBtn>
            <HBtn
              size="sm"
              @click="limitsDraft = null"
            >
              {{ tt('Cancel') }}
            </HBtn>
          </form>
        </div>

        <div class="flex flex-wrap gap-1.5">
          <HBtn
            size="sm"
            @click="openSession(run.coordinatorSessionId)"
          >
            {{ tt('Open coordinator') }}
          </HBtn>
          <template v-if="!runEnded(run.state)">
            <HBtn
              v-if="run.state === ProjectRunState.PAUSED"
              variant="primary"
              size="sm"
              @click="control(run, ControlProjectRunRequest_Action.RESUME)"
            >
              {{ tt('Resume') }}
            </HBtn>
            <HBtn
              v-else
              size="sm"
              @click="control(run, ControlProjectRunRequest_Action.PAUSE)"
            >
              {{ tt('Pause') }}
            </HBtn>
            <HBtn
              size="sm"
              @click="openRun = run.id; editLimits(run)"
            >
              {{ tt('Limits') }}
            </HBtn>
            <HBtn
              size="sm"
              tone="danger"
              @click="control(run, ControlProjectRunRequest_Action.STOP)"
            >
              {{ tt('Stop') }}
            </HBtn>
          </template>
          <HBtn
            v-if="(runEnded(run.state) || run.state === ProjectRunState.PAUSED)"
            size="sm"
            variant="ghost"
            tone="muted"
            @click="remove(run)"
          >
            {{ tt('Forget') }}
          </HBtn>
        </div>
      </article>
    </div>
  </section>
</template>
