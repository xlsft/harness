<script setup lang="ts">
// One issue: its text and fields, the linked external issue read live, comments, relations and history, and starting a session from it.
import { openExternal } from '~/lib/desktop'
import { errorText, timeAgo } from '~/lib/library/types'
import { CardRelationKind, externalNote, outputNote, PRIORITIES, relationLabel, RELATIONS, TrackerConnectionScope, type CardDetail, type Board, type ProjectMember, type ProjectScope } from '~/lib/issues/types'

const props = defineProps<{ id: string, projectScope: ProjectScope }>()
const emit = defineEmits<{ close: [], changed: [] }>()

const store = useSessionStore()
const detail = ref<CardDetail | null>(null)
const board = ref<Board | null>(null)
const members = ref<ProjectMember[]>([])
const error = ref('')
const comment = ref('')
const relKind = ref(String(CardRelationKind.BLOCKED_BY))
const relOther = ref('')
const confirming = ref(false)

const api = () => store.transport?.issues()

async function load() {
  const a = api()
  if (!a) return
  try {
    const d = await a.getCard(props.id)
    const e = d.card.external
    // The server can't read what is on a connection of this device; this client can.
    if (!d.issue && e?.connection?.scope === TrackerConnectionScope.DEVICE) {
      try {
        d.issue = await a.deviceIssue(e.connection.id, e.externalId)
      } catch (err) {
        d.issueError = errorText(err)
      }
    }
    detail.value = d
    board.value = (await a.getBoard(d.card.boardId)).board
    members.value = await a.listMembers(d.card.projectId).catch(() => [])
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  }
}
onMounted(load)
watch(() => props.id, load)

async function act(fn: () => Promise<unknown>) {
  try {
    await fn()
    await load()
    emit('changed')
  } catch (e) {
    error.value = errorText(e)
  }
}

const card = computed(() => detail.value?.card)
const canEdit = computed(() => !!board.value?.canEdit)
const save = (patch: Parameters<NonNullable<ReturnType<typeof api>>['updateCard']>[1]) => act(() => api()!.updateCard(card.value!.id, patch))
const dueDate = computed(() => card.value?.dueAt ? card.value.dueAt.slice(0, 10) : '')
const nameOf = (id: string) => members.value.find(m => m.userId === id)?.name || id

function toggleAssignee(id: string) {
  const cur = card.value?.assigneeIds ?? []
  save({ assigneeIds: cur.includes(id) ? cur.filter(x => x !== id) : [...cur, id] })
}

const addComment = () => act(async () => {
  if (!comment.value.trim()) return
  await api()!.comment(card.value!.id, comment.value)
  comment.value = ''
})

const addRelation = () => act(async () => {
  if (!relOther.value.trim()) return
  await api()!.relate(card.value!.id, Number(relKind.value), relOther.value.trim())
  relOther.value = ''
})

async function remove() {
  if (!confirming.value) {
    confirming.value = true
    return
  }
  try {
    await api()!.deleteCard(card.value!.id)
    emit('changed')
    emit('close')
  } catch (e) {
    error.value = errorText(e)
  }
}

// The new session screen takes it from here: folder, branch and first message come from the client.
function startSession() {
  navigateTo({ path: '/', query: { new: '1', issue: card.value!.id } })
}

function openSession(id: string) {
  store.open(id)
  navigateTo({ path: '/', query: { session: id } })
}

const stageOptions = computed(() => (board.value?.stages ?? []).map(s => ({ value: s.id, label: s.name })))
const priorityOptions = PRIORITIES.map(p => ({ value: String(p.value), label: p.label }))
const relationOptions = RELATIONS.map(r => ({ value: String(r.value), label: r.label }))
const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <aside
    :aria-label="tt('Issue')"
    class="fixed inset-y-0 right-0 z-30 flex w-[480px] max-w-full flex-col gap-3 overflow-y-auto border-l border-line bg-default p-5"
  >
    <div class="flex items-center gap-2">
      <span class="text-[12px] text-muted">{{ card?.key }}</span>
      <button
        v-if="card?.external?.url"
        type="button"
        class="text-[12px] text-accent hover:underline"
        @click="openExternal(card.external.url)"
      >
        {{ tt('Open in the tracker') }}
      </button>
      <span class="flex-1" />
      <HBtn
        size="sm"
        variant="ghost"
        @click="emit('close')"
      >
        {{ tt('Close') }}
      </HBtn>
    </div>
    <p
      v-if="error"
      role="alert"
      class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
    >
      {{ error }}
    </p>
    <template v-if="card">
      <input
        :value="card.title"
        :aria-label="tt('Title')"
        :readonly="!canEdit"
        class="border border-line bg-transparent px-2 py-1 text-[15px] font-medium text-strong outline-none focus:border-accent"
        @change="save({ title: ($event.target as HTMLInputElement).value })"
      >
      <p
        v-if="externalNote(card)"
        class="m-0 text-[12px] text-bad"
      >
        {{ externalNote(card) }}
      </p>
      <p
        v-if="outputNote(card).text"
        role="status"
        class="m-0 flex items-center gap-2 text-[12px]"
        :class="outputNote(card).bad ? 'text-bad' : 'text-muted'"
      >
        {{ outputNote(card).text }}
        <HBtn
          v-if="card.output?.status === 4"
          size="sm"
          @click="act(() => api()!.retryOutput(card!.id))"
        >
          {{ tt('Retry') }}
        </HBtn>
      </p>
      <div class="flex flex-wrap items-center gap-2 text-[12px] text-toned">
        <HSelect
          :model-value="card.stageId"
          class="w-[150px]"
          :options="stageOptions"
          :aria-label="tt('Stage')"
          :disabled="!canEdit"
          @update:model-value="act(() => api()!.moveCard(card!.id, { stageId: String($event), expectedVersion: card!.version }))"
        />
        <HSelect
          :model-value="String(card.priority)"
          class="w-[110px]"
          :options="priorityOptions"
          :aria-label="tt('Priority')"
          :disabled="!canEdit"
          @update:model-value="save({ priority: Number($event) })"
        />
        <label class="flex items-center gap-1.5">
          {{ tt('Due') }}
          <input
            type="date"
            :value="dueDate"
            :readonly="!canEdit"
            :class="inputClass"
            @change="save({ dueAt: ($event.target as HTMLInputElement).value ? new Date(($event.target as HTMLInputElement).value).toISOString() : '' })"
          >
        </label>
      </div>
      <input
        :value="card.labels.join(', ')"
        :placeholder="tt('Labels, separated by commas')"
        :aria-label="tt('Labels')"
        :readonly="!canEdit"
        :class="inputClass"
        @change="save({ labels: ($event.target as HTMLInputElement).value.split(',').map(s => s.trim()).filter(Boolean) })"
      >
      <div class="flex flex-wrap items-center gap-1.5 text-[12px] text-muted">
        <span>{{ tt('Assignees:') }}</span>
        <button
          v-for="m in members"
          :key="m.userId"
          type="button"
          class="border px-1.5 py-0.5"
          :class="card.assigneeIds.includes(m.userId) ? 'border-accent text-strong' : 'border-line text-toned'"
          :aria-pressed="card.assigneeIds.includes(m.userId)"
          :disabled="!canEdit"
          @click="toggleAssignee(m.userId)"
        >
          {{ m.name || m.email }}
        </button>
        <span v-if="!members.length">{{ card.assigneeIds.map(nameOf).join(', ') || tt('nobody yet') }}</span>
      </div>
      <textarea
        :value="card.description"
        :placeholder="tt('Details')"
        :aria-label="tt('Details')"
        :readonly="!canEdit"
        rows="5"
        class="resize-y border border-line bg-transparent p-2 text-[12.5px] text-strong outline-none focus:border-accent"
        @change="save({ description: ($event.target as HTMLTextAreaElement).value })"
      />

      <div
        v-if="detail?.issue"
        class="flex flex-col gap-1 border border-line p-2.5"
      >
        <h3 class="m-0 text-[12px] font-medium text-muted">
          {{ tt('Linked issue {0}, read now', [detail.issue.id]) }}
        </h3>
        <p class="m-0 text-[12.5px] text-strong">
          {{ detail.issue.title }}
        </p>
        <p
          v-for="f in detail.issue.fields"
          :key="f.name"
          class="m-0 text-[11.5px] text-toned"
        >
          {{ f.name }}: {{ f.value }}
        </p>
        <p class="m-0 whitespace-pre-wrap text-[12px] text-toned">
          {{ detail.issue.description }}
        </p>
        <p
          v-for="(c, i) in detail.issue.comments"
          :key="i"
          class="m-0 whitespace-pre-wrap border-t border-line pt-1 text-[12px] text-toned"
        >
          <b>{{ c.author }}</b> {{ c.text }}
        </p>
      </div>
      <p
        v-else-if="detail?.issueError"
        class="m-0 text-[12px] text-bad"
      >
        {{ tt('The linked issue could not be read: {0}', [detail.issueError]) }}
      </p>

      <div class="flex flex-wrap items-center gap-2 text-[12px] text-muted">
        <HBtn
          size="sm"
          variant="primary"
          @click="startSession"
        >
          {{ tt('Start a session') }}
        </HBtn>
        <span v-if="card.branch">{{ tt('Branch {0}', [card.branch]) }}</span>
        <button
          v-for="(s, i) in card.sessionIds"
          :key="s"
          type="button"
          class="text-accent hover:underline"
          @click="openSession(s)"
        >
          {{ store.sessions[s]?.meta.title || `session ${i + 1}` }}
        </button>
        <button
          v-for="u in card.prUrls"
          :key="u"
          type="button"
          class="text-accent hover:underline"
          @click="openExternal(u)"
        >
          {{ u.split('/').slice(-2).join('/') }}
        </button>
      </div>

      <div class="flex flex-col gap-1.5">
        <h3 class="m-0 text-[12px] font-medium text-muted">
          {{ tt('Relations') }}
        </h3>
        <div
          v-for="r in card.relations"
          :key="`${r.kind}:${r.cardId}`"
          class="flex items-center gap-2 text-[12.5px] text-toned"
        >
          <span class="text-muted">{{ relationLabel(r.kind) }}</span>
          <span class="min-w-0 flex-1 truncate">{{ r.key }} {{ r.title }}</span>
          <button
            v-if="canEdit"
            type="button"
            class="text-muted hover:text-strong"
            :aria-label="tt('Remove {0}', [r.key])"
            @click="act(() => api()!.relate(card!.id, r.kind, r.cardId, true))"
          >
            ×
          </button>
        </div>
        <form
          v-if="canEdit"
          class="flex items-center gap-2"
          @submit.prevent="addRelation"
        >
          <HSelect
            v-model="relKind"
            class="w-[120px]"
            :options="relationOptions"
            :aria-label="tt('Relation')"
          />
          <input
            v-model="relOther"
            :placeholder="tt('Another issue, such as ABC-12')"
            :aria-label="tt('Other issue')"
            :class="[inputClass, 'min-w-0 flex-1']"
          >
          <HBtn
            size="sm"
            type="submit"
          >
            {{ tt('Add') }}
          </HBtn>
        </form>
      </div>

      <div class="flex flex-col gap-1.5">
        <h3 class="m-0 text-[12px] font-medium text-muted">
          {{ tt('Comments') }}
        </h3>
        <div
          v-for="c in detail?.comments"
          :key="c.id"
          class="flex flex-col gap-0.5 border-b border-line pb-1.5"
        >
          <span class="flex items-center gap-2 text-[11.5px] text-muted">
            {{ c.authorName }} · {{ timeAgo(c.createdAt) }}
            <button
              type="button"
              class="hover:text-strong"
              :aria-label="tt('Remove this comment')"
              @click="act(() => api()!.deleteComment(c.id))"
            >×</button>
          </span>
          <p class="m-0 whitespace-pre-wrap text-[12.5px] text-toned">
            {{ c.text }}
          </p>
        </div>
        <form
          v-if="canEdit"
          class="flex flex-col gap-1.5"
          @submit.prevent="addComment"
        >
          <textarea
            v-model="comment"
            :placeholder="tt('Add a comment')"
            :aria-label="tt('Add a comment')"
            rows="2"
            class="resize-y border border-line bg-transparent p-2 text-[12.5px] text-strong outline-none focus:border-accent"
          />
          <HBtn
            size="sm"
            type="submit"
            class="self-start"
            :disabled="!comment.trim()"
          >
            {{ tt('Comment') }}
          </HBtn>
        </form>
      </div>

      <details class="text-[12px] text-muted">
        <summary class="cursor-pointer">
          {{ tt('History') }}
        </summary>
        <p
          v-for="a in detail?.activity"
          :key="a.id"
          class="m-0 py-0.5"
        >
          {{ a.actorName || tt('Sync') }}: {{ a.summary }} · {{ timeAgo(a.createdAt) }}
        </p>
      </details>

      <HBtn
        v-if="canEdit"
        size="sm"
        tone="danger"
        class="self-start"
        @click="remove"
      >
        {{ confirming ? tt('Delete for good') : tt('Delete') }}
      </HBtn>
    </template>
  </aside>
</template>
