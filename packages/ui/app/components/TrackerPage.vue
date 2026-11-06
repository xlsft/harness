<script setup lang="ts">
// Task tracker: the user's own tasks across sessions and machines. Agents keep the same list with their TaskTracker tool;
// this screen and the tool both go to the server, which alone says what a task is.
import { errorText, groupTasks, statusLabel, TASK_PRIORITIES, TASK_STATUSES, timeAgo, TrackerPriority, TrackerStatus, type TrackerTask } from '~/lib/library/types'

const store = useSessionStore()
const tasks = ref<TrackerTask[]>([])
const loaded = ref(false)
const error = ref('')
const query = ref('')
const title = ref('')
const project = ref('')
const showClosed = ref(false)
const openId = ref('')

const api = () => store.transport?.library()

async function load() {
  const a = api()
  if (!a) return
  try {
    tasks.value = await a.listTasks({ query: query.value })
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loaded.value = true
  }
}
onMounted(load)
watch(() => store.transport, load)
let timer: ReturnType<typeof setTimeout> | undefined
watch(query, () => {
  clearTimeout(timer)
  timer = setTimeout(load, 200)
})
onBeforeUnmount(() => clearTimeout(timer))

const CLOSED = [TrackerStatus.DONE, TrackerStatus.CANCELLED]
const columns = computed(() => groupTasks(tasks.value).filter(c => showClosed.value || !CLOSED.includes(c.status) || !!query.value))
const closedCount = computed(() => tasks.value.filter(t => CLOSED.includes(t.status)).length)

async function act(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  }
}

const add = () => act(async () => {
  if (!title.value.trim()) return
  await api()?.createTask({ title: title.value, project: project.value })
  title.value = ''
  await load()
})

function replace(t: TrackerTask | undefined) {
  if (!t) return
  tasks.value = tasks.value.map(x => (x.id === t.id ? t : x))
}

const setStatus = (t: TrackerTask, status: TrackerStatus) => act(async () => replace(await api()?.updateTask(t.id, { status })))
const setPriority = (t: TrackerTask, priority: TrackerPriority) => act(async () => replace(await api()?.updateTask(t.id, { priority })))
const saveField = (t: TrackerTask, patch: { title?: string, description?: string, project?: string }) => act(async () => replace(await api()?.updateTask(t.id, patch)))

const confirming = ref('')
const remove = (t: TrackerTask) => act(async () => {
  if (confirming.value !== t.id) {
    confirming.value = t.id
    return
  }
  await api()?.deleteTask(t.id)
  confirming.value = ''
  openId.value = ''
  await load()
})

const statusOptions = TASK_STATUSES.map(s => ({ value: String(s.status), label: s.label }))
const priorityOptions = TASK_PRIORITIES.map(p => ({ value: String(p.priority), label: p.label }))
const titleOf = (id: string) => tasks.value.find(t => t.id === id)?.title ?? id

function openSession(id: string) {
  store.open(id)
  navigateTo({ path: '/', query: { session: id } })
}
</script>

<template>
  <section
    :aria-label="tt('Task tracker')"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[860px] flex-col gap-[18px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <h1 class="m-0 font-display text-[26px] font-bold text-strong">
        {{ tt('Task tracker') }}
      </h1>
      <p class="m-0 text-[12.5px] text-muted">
        {{ tt('Your tasks across sessions and machines. Agents read and update them too, and nobody else can see them.') }}
      </p>

      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>

      <form
        class="flex flex-wrap items-center gap-2"
        @submit.prevent="add"
      >
        <input
          v-model="title"
          :placeholder="tt('A new task')"
          :aria-label="tt('New task')"
          class="min-w-[220px] flex-1 border border-line bg-transparent px-2.5 py-1.5 text-[13px] text-strong outline-none focus:border-accent"
        >
        <input
          v-model="project"
          :placeholder="tt('Project')"
          :aria-label="tt('Project')"
          class="w-[140px] border border-line bg-transparent px-2.5 py-1.5 text-[13px] text-strong outline-none focus:border-accent"
        >
        <HBtn
          size="md"
          variant="primary"
          type="submit"
          :disabled="!title.trim()"
        >
          {{ tt('Add') }}
        </HBtn>
      </form>

      <div class="flex flex-wrap items-center gap-3">
        <label class="flex h-8 flex-1 items-center gap-2 border border-line px-2.5 text-muted focus-within:border-accent">
          <HIcon
            name="search"
            :size="13"
          />
          <input
            v-model="query"
            :placeholder="tt('Search tasks')"
            :aria-label="tt('Search tasks')"
            class="min-w-0 flex-1 bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
          >
        </label>
        <label class="flex items-center gap-1.5 text-[12px] text-toned">
          <HToggle
            v-model="showClosed"
            :label="tt('Show closed tasks')"
          />
          {{ tt('Closed ({0})', [closedCount]) }}
        </label>
      </div>

      <p
        v-if="loaded && !tasks.length"
        class="m-0 text-[12.5px] text-muted"
      >
        {{ query ? tt('Nothing matches.') : tt('No tasks yet. Add one here, or ask an agent to note something for later.') }}
      </p>

      <template
        v-for="c in columns"
        :key="c.status"
      >
        <div
          v-if="c.tasks.length"
          class="flex flex-col"
        >
          <h2 class="m-0 pb-1.5 text-[12px] font-medium text-muted">
            {{ c.label }} · {{ c.tasks.length }}
          </h2>
          <div
            v-for="t in c.tasks"
            :key="t.id"
            class="flex flex-col border-b border-line"
          >
            <div class="flex items-center gap-2 py-2">
              <button
                type="button"
                class="flex size-5 shrink-0 items-center justify-center text-muted hover:text-strong"
                :aria-label="t.status === TrackerStatus.DONE ? tt('Reopen {0}', [t.title]) : tt('Mark {0} as done', [t.title])"
                @click="setStatus(t, t.status === TrackerStatus.DONE ? TrackerStatus.TODO : TrackerStatus.DONE)"
              >
                <HIcon :name="t.status === TrackerStatus.DONE ? 'checkbox-checked' : 'checkbox'" />
              </button>
              <button
                type="button"
                class="flex min-w-0 flex-1 items-center gap-2 text-left"
                :aria-expanded="openId === t.id"
                @click="openId = openId === t.id ? '' : t.id"
              >
                <span
                  class="min-w-0 truncate text-[13px]"
                  :class="CLOSED.includes(t.status) ? 'text-muted line-through' : 'text-strong'"
                >{{ t.title }}</span>
                <span
                  v-if="t.priority === TrackerPriority.HIGH"
                  class="shrink-0 border border-bad px-1 text-[10px] leading-[14px] text-bad"
                >{{ tt('high') }}</span>
                <span
                  v-if="t.project"
                  class="shrink-0 border border-default px-1 text-[10px] leading-[14px] text-muted"
                >{{ t.project }}</span>
                <span
                  v-if="t.blockedBy.length"
                  class="shrink-0 text-[11px] text-muted"
                >{{ tt('blocked by {0}', [t.blockedBy.length]) }}</span>
              </button>
              <span class="shrink-0 text-[11.5px] text-muted">{{ timeAgo(t.updatedAt) }}</span>
            </div>
            <div
              v-if="openId === t.id"
              class="flex flex-col gap-2 pb-3 pl-7"
            >
              <input
                :value="t.title"
                :aria-label="tt('Title')"
                class="border border-line bg-transparent px-2 py-1 text-[13px] text-strong outline-none focus:border-accent"
                @change="saveField(t, { title: ($event.target as HTMLInputElement).value })"
              >
              <textarea
                :value="t.description"
                :placeholder="tt('Details')"
                :aria-label="tt('Details')"
                rows="4"
                class="resize-y border border-line bg-transparent p-2 text-[12.5px] text-strong outline-none focus:border-accent"
                @change="saveField(t, { description: ($event.target as HTMLTextAreaElement).value })"
              />
              <div class="flex flex-wrap items-center gap-2 text-[12px] text-toned">
                <HSelect
                  :model-value="String(t.status)"
                  class="w-[130px]"
                  :options="statusOptions"
                  :aria-label="tt('Status')"
                  @update:model-value="setStatus(t, Number($event))"
                />
                <HSelect
                  :model-value="String(t.priority)"
                  class="w-[110px]"
                  :options="priorityOptions"
                  :aria-label="tt('Priority')"
                  @update:model-value="setPriority(t, Number($event))"
                />
                <input
                  :value="t.project"
                  :placeholder="tt('Project')"
                  :aria-label="tt('Project of the task')"
                  class="w-[140px] border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
                  @change="saveField(t, { project: ($event.target as HTMLInputElement).value })"
                >
                <span class="flex-1" />
                <HBtn
                  size="sm"
                  tone="danger"
                  @click="remove(t)"
                >
                  {{ confirming === t.id ? tt('Delete for good') : tt('Delete') }}
                </HBtn>
              </div>
              <p
                v-if="t.blockedBy.length"
                class="m-0 text-[12px] text-muted"
              >
                {{ tt('Waits for: {0}', [t.blockedBy.map(titleOf).join(', ')]) }}
              </p>
              <div
                v-if="t.sessionIds.length"
                class="flex flex-wrap items-center gap-1.5 text-[12px] text-muted"
              >
                <span>{{ tt('Sessions:') }}</span>
                <button
                  v-for="(s, i) in t.sessionIds"
                  :key="s"
                  type="button"
                  class="text-accent hover:underline"
                  @click="openSession(s)"
                >
                  {{ store.sessions[s]?.meta.title || `session ${i + 1}` }}
                </button>
              </div>
              <p class="m-0 text-[11.5px] text-muted">
                {{ tt('{0} · created {1}', [statusLabel(t.status), timeAgo(t.createdAt)]) }}
              </p>
            </div>
          </div>
        </div>
      </template>
    </div>
  </section>
</template>
