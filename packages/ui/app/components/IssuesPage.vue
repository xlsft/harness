<script setup lang="ts">
// Issues: projects, their boards as columns you drag between, and a list with filters and saved views. The server decides what the
// caller sees and may do; `canEdit` and `canConfigure` only hide what it would refuse.
import { openExternal } from '~/lib/desktop'
import { errorText } from '~/lib/library/types'
import {
  BoardScope,
  cardsByStage,
  CardPriority,
  externalNote,
  outputNote,
  priorityLabel,
  PRIORITIES,
  ProjectLevel,
  ProjectScope,
  type Board,
  type Card,
  type CardFilterInit,
  type Project,
  type SavedView
} from '~/lib/issues/types'

const store = useSessionStore()
const route = useRoute()
const projects = ref<Project[]>([])
const boards = ref<Board[]>([])
const projectId = ref('')
const boardId = ref('')
const board = ref<Board | null>(null)
const cards = ref<Card[]>([])
const list = ref<Card[]>([])
const views = ref<SavedView[]>([])
const mode = ref<'board' | 'list'>('board')
const error = ref('')
const loaded = ref(false)
const openId = ref('')
const editing = ref<'' | 'board' | 'newProject' | 'project'>('')

const query = ref('')
const mine = ref(false)
const priority = ref('')
const label = ref('')
const sort = ref('updated')

const api = () => store.transport?.issues()
const project = computed(() => projects.value.find(p => p.id === projectId.value))
const columns = computed(() => cardsByStage(board.value?.stages ?? [], cards.value))

async function act(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  }
}

async function loadProjects() {
  const a = api()
  if (!a) return
  await act(async () => {
    projects.value = await a.listProjects()
    if (!projects.value.length) projects.value = [await a.ensureDefaultProject()]
    views.value = await a.listViews()
    if (!projects.value.some(p => p.id === projectId.value)) projectId.value = projects.value[0]?.id ?? ''
    await loadBoards()
  })
  loaded.value = true
}

async function loadBoards() {
  const a = api()
  if (!a || !projectId.value) return
  boards.value = await a.listBoards(projectId.value)
  if (!boards.value.some(b => b.id === boardId.value)) boardId.value = boards.value[0]?.id ?? ''
  await loadBoard()
}

async function loadBoard() {
  const a = api()
  if (!a) return
  if (!boardId.value) {
    board.value = null
    cards.value = []
  } else {
    const r = await a.getBoard(boardId.value)
    board.value = r.board
    cards.value = r.cards
  }
  await loadList()
}

const filter = computed((): CardFilterInit => ({
  projectId: projectId.value,
  query: query.value,
  assigneeIds: mine.value ? ['me'] : [],
  priorities: priority.value ? [Number(priority.value) as CardPriority] : [],
  labels: label.value.trim() ? [label.value.trim()] : [],
  sort: sort.value
}))

async function loadList() {
  const a = api()
  if (!a || mode.value !== 'list') return
  list.value = await a.listCards(filter.value)
}

onMounted(() => {
  if (typeof route.query.card === 'string') openId.value = route.query.card
  void loadProjects()
})
watch(() => store.transport, loadProjects)
watch(projectId, () => act(loadBoards))
watch(boardId, () => act(loadBoard))
let timer: ReturnType<typeof setTimeout> | undefined
watch([filter, mode], () => {
  clearTimeout(timer)
  timer = setTimeout(() => act(loadList), 200)
})
onBeforeUnmount(() => clearTimeout(timer))

// ---- creating ----

const newTitle = ref<Record<string, string>>({})
const addCard = (stageId: string) => act(async () => {
  const title = (newTitle.value[stageId] ?? '').trim()
  if (!title || !board.value) return
  await api()?.createCard({ boardId: board.value.id, stageId, title })
  newTitle.value[stageId] = ''
  await loadBoard()
})

const newBoardName = ref('')
const newBoardShared = ref(false)
const canManage = computed(() => project.value?.level === ProjectLevel.MANAGE)
const addBoard = () => act(async () => {
  if (!newBoardName.value.trim() || !projectId.value) return
  const b = await api()?.createBoard({ projectId: projectId.value, scope: newBoardShared.value ? BoardScope.SHARED : BoardScope.PERSONAL, name: newBoardName.value.trim() })
  newBoardName.value = ''
  await loadBoards()
  if (b) boardId.value = b.id
})

// ---- dragging ----

const dragging = ref<Card | null>(null)
const over = ref('')

function onDragStart(e: DragEvent, c: Card) {
  dragging.value = c
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', c.key)
  }
}

const moveTo = (c: Card, stageId: string, beforeCardId = '') => act(async () => {
  if (c.id === beforeCardId) return
  try {
    await api()?.moveCard(c.id, { stageId, beforeCardId, expectedVersion: c.version })
  } finally {
    await loadBoard()
  }
})

function onDrop(stageId: string, beforeCardId = '') {
  const c = dragging.value
  dragging.value = null
  over.value = ''
  if (c) void moveTo(c, stageId, beforeCardId)
}

// ---- views ----

const viewName = ref('')
const saveView = () => act(async () => {
  if (!viewName.value.trim()) return
  await api()?.saveView(viewName.value.trim(), filter.value)
  viewName.value = ''
  views.value = (await api()?.listViews()) ?? []
})

function applyView(v: SavedView) {
  const f = v.filter
  mode.value = 'list'
  if (f?.projectId) projectId.value = f.projectId
  query.value = f?.query ?? ''
  mine.value = (f?.assigneeIds ?? []).includes('me')
  priority.value = f?.priorities?.[0] ? String(f.priorities[0]) : ''
  label.value = f?.labels?.[0] ?? ''
  sort.value = f?.sort || 'updated'
}

const removeView = (v: SavedView) => act(async () => {
  await api()?.deleteView(v.id)
  views.value = (await api()?.listViews()) ?? []
})

async function changed() {
  await act(async () => {
    projects.value = (await api()?.listProjects()) ?? []
    await loadBoards()
  })
}

const priorityOptions = [{ value: '', label: tt('Any priority') }, ...PRIORITIES.map(p => ({ value: String(p.value), label: p.label }))]
const sortOptions = [
  { value: 'updated', label: tt('Recently updated') },
  { value: 'created', label: tt('Recently created') },
  { value: 'priority', label: tt('Priority') },
  { value: 'due', label: tt('Due date') }
]
const projectOptions = computed(() => projects.value.map(p => ({ value: p.id, label: `${p.metadata?.ticker} · ${p.metadata?.name}` })))
const boardOptions = computed(() => boards.value.map(b => ({ value: b.id, label: b.scope === BoardScope.PERSONAL ? tt('{0} (yours)', [b.name]) : b.name })))
const stageName = (id: string) => board.value?.stages.find(s => s.id === id)?.name ?? ''
</script>

<template>
  <section
    :aria-label="tt('Issues')"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex flex-col gap-3 px-10 pt-[52px] pb-3 max-sm:px-4">
      <div class="flex flex-wrap items-center gap-2">
        <h1 class="m-0 mr-2 font-display text-[26px] font-bold text-strong">
          {{ tt('Issues') }}
        </h1>
        <span
          v-if="project?.metadata?.logo && !project.metadata.logo.startsWith('http')"
          aria-hidden="true"
        >{{ project.metadata.logo }}</span>
        <img
          v-else-if="project?.metadata?.logo"
          :src="project.metadata.logo"
          alt=""
          class="size-5"
        >
        <span
          v-if="project?.metadata?.color"
          class="size-2.5 rounded-full"
          :style="{ background: project.metadata.color }"
          aria-hidden="true"
        />
        <HSelect
          v-if="projectOptions.length"
          v-model="projectId"
          class="w-[220px]"
          :options="projectOptions"
          :aria-label="tt('Project')"
        />
        <HBtn
          size="sm"
          variant="ghost"
          @click="editing = editing === 'project' ? '' : 'project'"
        >
          {{ tt('Project settings') }}
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          @click="editing = editing === 'newProject' ? '' : 'newProject'"
        >
          {{ tt('New project') }}
        </HBtn>
        <span class="flex-1" />
        <div
          role="tablist"
          :aria-label="tt('View')"
          class="flex gap-[2px]"
        >
          <button
            v-for="m in [{ v: 'board', l: tt('Board') }, { v: 'list', l: tt('List') }]"
            :key="m.v"
            type="button"
            role="tab"
            :aria-selected="mode === m.v"
            class="border px-3 py-[5px] text-[12.5px]"
            :class="mode === m.v ? 'border-line bg-raised text-strong' : 'border-transparent text-toned hover:text-strong'"
            @click="mode = m.v as 'board' | 'list'"
          >
            {{ m.l }}
          </button>
        </div>
      </div>

      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>

      <IssuesProjectForm
        v-if="editing === 'project' && project"
        :key="project.id"
        :project="project"
        @close="editing = ''"
        @changed="changed"
      />
      <IssuesProjectForm
        v-else-if="editing === 'newProject'"
        @close="editing = ''"
        @created="id => { projectId = id; editing = ''; changed() }"
      />

      <div
        v-if="mode === 'board'"
        class="flex flex-wrap items-center gap-2"
      >
        <HSelect
          v-if="boardOptions.length"
          v-model="boardId"
          class="w-[220px]"
          :options="boardOptions"
          :aria-label="tt('Board')"
        />
        <HBtn
          v-if="board?.canConfigure"
          size="sm"
          variant="ghost"
          @click="editing = editing === 'board' ? '' : 'board'"
        >
          {{ tt('Board settings') }}
        </HBtn>
        <HBtn
          v-if="board"
          size="sm"
          variant="ghost"
          @click="act(async () => { await api()?.syncBoard(board!.id); await loadBoard() })"
        >
          {{ tt('Sync now') }}
        </HBtn>
        <span class="flex-1" />
        <form
          class="flex items-center gap-2"
          @submit.prevent="addBoard"
        >
          <input
            v-model="newBoardName"
            :placeholder="tt('New board')"
            :aria-label="tt('New board')"
            class="w-[150px] border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
          >
          <label
            v-if="canManage"
            class="flex items-center gap-1.5 text-[12px] text-toned"
          >
            <HToggle
              v-model="newBoardShared"
              :label="tt('Shared with the project')"
            />
            {{ tt('Shared') }}
          </label>
          <HBtn
            size="sm"
            type="submit"
            :disabled="!newBoardName.trim()"
          >
            {{ tt('Add') }}
          </HBtn>
        </form>
      </div>

      <div
        v-else
        class="flex flex-wrap items-center gap-2"
      >
        <label class="flex h-8 items-center gap-2 border border-line px-2.5 text-muted focus-within:border-accent">
          <HIcon
            name="search"
            :size="13"
          />
          <input
            v-model="query"
            :placeholder="tt('Search issues')"
            :aria-label="tt('Search issues')"
            class="w-[180px] bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
          >
        </label>
        <HSelect
          v-model="priority"
          class="w-[130px]"
          :options="priorityOptions"
          :aria-label="tt('Priority')"
        />
        <input
          v-model="label"
          :placeholder="tt('Label')"
          :aria-label="tt('Label')"
          class="w-[110px] border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
        >
        <label class="flex items-center gap-1.5 text-[12px] text-toned">
          <HToggle
            v-model="mine"
            :label="tt('Assigned to me')"
          />
          {{ tt('Mine') }}
        </label>
        <HSelect
          v-model="sort"
          class="w-[150px]"
          :options="sortOptions"
          :aria-label="tt('Sort')"
        />
        <span class="flex-1" />
        <form
          class="flex items-center gap-2"
          @submit.prevent="saveView"
        >
          <input
            v-model="viewName"
            :placeholder="tt('Save this view as')"
            :aria-label="tt('Name of the view')"
            class="w-[130px] border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
          >
          <HBtn
            size="sm"
            type="submit"
            :disabled="!viewName.trim()"
          >
            {{ tt('Save') }}
          </HBtn>
        </form>
      </div>
      <div
        v-if="mode === 'list' && views.length"
        class="flex flex-wrap items-center gap-1.5"
      >
        <span
          v-for="v in views"
          :key="v.id"
          class="flex items-center gap-1 border border-line px-2 py-0.5 text-[12px] text-toned"
        >
          <button
            type="button"
            class="hover:text-strong"
            @click="applyView(v)"
          >{{ v.name }}</button>
          <button
            type="button"
            class="text-muted hover:text-strong"
            :aria-label="tt('Remove {0}', [v.name])"
            @click="removeView(v)"
          >×</button>
        </span>
      </div>

      <IssuesBoardForm
        v-if="editing === 'board' && board"
        :key="board.id"
        :board="board"
        :project="project"
        @close="editing = ''"
        @changed="() => { editing = ''; changed() }"
      />
    </div>

    <div
      v-if="mode === 'board'"
      class="flex min-h-0 flex-1 gap-3 overflow-x-auto px-10 pb-6 max-sm:px-4"
    >
      <p
        v-if="loaded && !board"
        class="m-0 text-[12.5px] text-muted"
      >
        {{ tt('This project has no board you can see yet. Add one above.') }}
      </p>
      <div
        v-for="s in board?.stages ?? []"
        :key="s.id"
        class="flex w-[280px] shrink-0 flex-col border bg-raised/40"
        :class="over === s.id ? 'border-accent' : 'border-line'"
        @dragover.prevent="over = s.id"
        @dragleave="over = over === s.id ? '' : over"
        @drop.prevent="onDrop(s.id)"
      >
        <div class="flex flex-col gap-0.5 border-b border-line px-2.5 py-2">
          <h2 class="m-0 text-[12.5px] font-medium text-strong">
            {{ s.name }} <span class="text-muted">· {{ columns[s.id]?.length ?? 0 }}</span>
          </h2>
          <span
            v-for="i in s.inputs"
            :key="i.id"
            class="truncate text-[11px]"
            :class="i.lastError ? 'text-bad' : 'text-muted'"
            :title="i.lastError || i.query"
          >{{ i.lastError ? tt('Input failing: {0}', [i.lastError]) : tt('From {0}', [i.query]) }}</span>
          <span
            v-for="o in s.outputs"
            :key="o.id"
            class="truncate text-[11px] text-muted"
          >{{ tt('Sets {0}', [o.fields.map(f => `${f.name}: ${f.value}`).join(', ') || tt('a comment')]) }}</span>
        </div>
        <div class="flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto p-1.5">
          <article
            v-for="c in columns[s.id]"
            :key="c.id"
            :draggable="board?.canEdit"
            class="flex cursor-pointer flex-col gap-1 border border-line bg-default p-2 hover:border-line-strong"
            :class="dragging?.id === c.id ? 'opacity-50' : ''"
            @dragstart="onDragStart($event, c)"
            @dragover.prevent
            @drop.prevent.stop="onDrop(s.id, c.id)"
            @click="openId = c.id"
          >
            <span class="flex items-center gap-1.5 text-[11px] text-muted">
              <button
                v-if="c.external?.url"
                type="button"
                class="hover:underline"
                @click.stop="openExternal(c.external.url)"
              >{{ c.key }}</button>
              <span v-else>{{ c.key }}</span>
              <span
                v-if="c.priority === CardPriority.URGENT || c.priority === CardPriority.HIGH"
                class="border border-bad px-1 leading-[14px] text-bad"
              >{{ priorityLabel(c.priority) }}</span>
              <span v-if="c.commentCount">{{ tt('{0} comments', [c.commentCount]) }}</span>
            </span>
            <span class="text-[13px] text-strong">{{ c.title }}</span>
            <span
              v-if="c.labels.length"
              class="flex flex-wrap gap-1"
            >
              <span
                v-for="l in c.labels"
                :key="l"
                class="border border-default px-1 text-[10px] leading-[14px] text-muted"
              >{{ l }}</span>
            </span>
            <span
              v-if="externalNote(c)"
              class="text-[11px] text-bad"
            >{{ externalNote(c) }}</span>
            <span
              v-if="outputNote(c).text"
              class="flex items-center gap-1.5 text-[11px]"
              :class="outputNote(c).bad ? 'text-bad' : 'text-muted'"
              role="status"
            >
              {{ outputNote(c).text }}
              <button
                v-if="c.output?.status === 4"
                type="button"
                class="text-accent hover:underline"
                @click.stop="act(async () => { await api()?.retryOutput(c.id); await loadBoard() })"
              >{{ tt('Retry') }}</button>
            </span>
          </article>
        </div>
        <form
          v-if="board?.canEdit"
          class="border-t border-line p-1.5"
          @submit.prevent="addCard(s.id)"
        >
          <input
            v-model="newTitle[s.id]"
            :placeholder="tt('Add an issue')"
            :aria-label="tt('Add an issue to {0}', [s.name])"
            class="w-full bg-transparent px-1 py-0.5 text-[12.5px] text-strong outline-none placeholder:text-muted"
          >
        </form>
      </div>
    </div>

    <div
      v-else
      class="min-h-0 flex-1 overflow-y-auto px-10 pb-6 max-sm:px-4"
    >
      <p
        v-if="!list.length"
        class="m-0 text-[12.5px] text-muted"
      >
        {{ tt('Nothing matches.') }}
      </p>
      <button
        v-for="c in list"
        :key="c.id"
        type="button"
        class="flex w-full items-center gap-3 border-b border-line px-1 py-2 text-left hover:bg-hover"
        @click="openId = c.id"
      >
        <span class="w-[90px] shrink-0 text-[11.5px] text-muted">{{ c.key }}</span>
        <span class="min-w-0 flex-1 truncate text-[13px] text-strong">{{ c.title }}</span>
        <span
          v-if="outputNote(c).bad"
          class="shrink-0 text-[11px] text-bad"
        >{{ tt('Output failed') }}</span>
        <span class="w-[110px] shrink-0 truncate text-[11.5px] text-muted">{{ stageName(c.stageId) }}</span>
        <span class="w-[60px] shrink-0 text-[11.5px] text-muted">{{ priorityLabel(c.priority) }}</span>
      </button>
    </div>

    <IssueDetail
      v-if="openId"
      :id="openId"
      :project-scope="project?.scope ?? ProjectScope.PERSONAL"
      @close="openId = ''"
      @changed="act(loadBoard)"
    />
  </section>
</template>
