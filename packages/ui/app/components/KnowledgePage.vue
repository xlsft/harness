<script setup lang="ts">
// Knowledge base: documents on the server that agents search with KnowledgeSearch. Documents for everyone are written by
// admins; a user's own are theirs alone. The server enforces both: `editable` is its answer, and the choice of scope only
// appears for those it would accept.
import { errorText, formatSize, scopeLabel, Scope, timeAgo, type KnowledgeDocument, type KnowledgeHit } from '~/lib/library/types'

const store = useSessionStore()
const documents = ref<KnowledgeDocument[]>([])
const loaded = ref(false)
const error = ref('')
const isAdmin = ref(store.account.role === 'admin')

const SCOPES = [
  { label: 'All', value: Scope.UNSPECIFIED },
  { label: 'Everyone', value: Scope.ORG },
  { label: 'Only you', value: Scope.USER }
]
const filter = ref(Scope.UNSPECIFIED)
const search = ref('')
const hits = ref<KnowledgeHit[] | null>(null)

const api = () => store.transport?.library()

async function load() {
  const a = api()
  if (!a) return
  try {
    documents.value = await a.listDocuments({ scope: filter.value })
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loaded.value = true
  }
}
onMounted(async () => {
  await load()
  try {
    if (store.transport) isAdmin.value = await store.transport.users().isAdmin()
  } catch {
    // Keep what the shell said; the server refuses what is not allowed anyway.
  }
})
watch(() => store.transport, load)
watch(filter, load)

let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined
watch(search, (q) => {
  clearTimeout(timer)
  if (!q.trim()) {
    seq++
    hits.value = null
    return
  }
  timer = setTimeout(async () => {
    const n = ++seq
    try {
      const res = await api()?.searchKnowledge(q)
      if (n === seq) hits.value = res ?? []
    } catch (e) {
      if (n === seq) error.value = errorText(e)
    }
  }, 250)
})
onBeforeUnmount(() => clearTimeout(timer))

// The editor: a new document (id '') or an open one.
const editing = ref<{ id: string, title: string, content: string, scope: Scope, sourceName: string, editable: boolean } | null>(null)
const saving = ref(false)
const confirming = ref(false)

function newDocument() {
  confirming.value = false
  editing.value = { id: '', title: '', content: '', scope: Scope.USER, sourceName: '', editable: true }
}

async function openDocument(id: string) {
  confirming.value = false
  try {
    const res = await api()?.getDocument(id)
    if (!res?.document) return
    editing.value = { id, title: res.document.title, content: res.content, scope: res.document.scope, sourceName: res.document.sourceName, editable: res.document.editable }
  } catch (e) {
    error.value = errorText(e)
  }
}

async function pickFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || !editing.value) return
  const buffer = await file.arrayBuffer()
  try {
    // Text only: a PDF or a Word file would fail here rather than be stored as noise.
    editing.value.content = new TextDecoder('utf-8', { fatal: true }).decode(buffer)
  } catch {
    error.value = `${file.name} is not a text file. The knowledge base takes text and Markdown.`
    return
  }
  editing.value.sourceName = file.name
  if (!editing.value.title) editing.value.title = file.name.replace(/\.[^.]+$/, '')
  error.value = ''
}

async function save() {
  const d = editing.value
  const a = api()
  if (!d || !a) return
  saving.value = true
  try {
    if (d.id) await a.updateDocument(d.id, { title: d.title, content: d.content })
    else await a.createDocument({ scope: d.scope, title: d.title, content: d.content, sourceName: d.sourceName })
    editing.value = null
    error.value = ''
    await load()
  } catch (e) {
    error.value = errorText(e)
  } finally {
    saving.value = false
  }
}

async function remove() {
  const d = editing.value
  if (!d?.id) return
  if (!confirming.value) {
    confirming.value = true
    return
  }
  try {
    await api()?.deleteDocument(d.id)
    editing.value = null
    await load()
  } catch (e) {
    error.value = errorText(e)
  }
}
</script>

<template>
  <section
    aria-label="Knowledge base"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[860px] flex-col gap-[18px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <div class="flex items-center gap-3">
        <h1 class="m-0 flex-1 font-display text-[26px] font-bold text-strong">
          Knowledge base
        </h1>
        <HBtn
          size="md"
          variant="primary"
          @click="newDocument"
        >
          Add a document
        </HBtn>
      </div>
      <p class="m-0 text-[12.5px] text-muted">
        Agents search these with their KnowledgeSearch tool. Documents for everyone are added by admins; yours stay visible to you alone.
      </p>

      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>

      <form
        v-if="editing"
        class="flex flex-col gap-2.5 border border-line p-3.5"
        @submit.prevent="save"
      >
        <input
          v-model="editing.title"
          placeholder="Title"
          aria-label="Title"
          :readonly="!editing.editable"
          class="border border-line bg-transparent px-2.5 py-1.5 text-[13px] text-strong outline-none focus:border-accent"
        >
        <div
          v-if="!editing.id"
          class="flex flex-wrap items-center gap-3 text-[12.5px] text-toned"
        >
          <template v-if="isAdmin">
            <span>Visible to</span>
            <HSelect
              :model-value="String(editing.scope)"
              class="w-[150px]"
              :options="[{ value: String(Scope.USER), label: 'Only you' }, { value: String(Scope.ORG), label: 'Everyone' }]"
              aria-label="Visible to"
              @update:model-value="editing!.scope = Number($event)"
            />
          </template>
          <label class="cursor-pointer text-accent hover:underline">
            Load from a text file
            <input
              type="file"
              accept=".md,.markdown,.txt,.rst,.csv,.json,.yaml,.yml,.html,text/*"
              class="sr-only"
              @change="pickFile"
            >
          </label>
          <span
            v-if="editing.sourceName"
            class="text-muted"
          >{{ editing.sourceName }}</span>
        </div>
        <textarea
          v-model="editing.content"
          placeholder="Text or Markdown"
          aria-label="Content"
          rows="14"
          :readonly="!editing.editable"
          class="resize-y border border-line bg-transparent p-2.5 font-mono text-[12px] text-strong outline-none focus:border-accent"
        />
        <div class="flex items-center gap-2">
          <HBtn
            v-if="editing.editable"
            size="md"
            variant="primary"
            type="submit"
            :disabled="saving"
          >
            Save
          </HBtn>
          <HBtn
            size="md"
            variant="ghost"
            @click="editing = null"
          >
            {{ editing.editable ? 'Cancel' : 'Close' }}
          </HBtn>
          <span class="flex-1" />
          <HBtn
            v-if="editing.id && editing.editable"
            size="md"
            tone="danger"
            @click="remove"
          >
            {{ confirming ? 'Delete for good' : 'Delete' }}
          </HBtn>
        </div>
      </form>

      <div class="flex flex-wrap items-center gap-2">
        <div
          role="tablist"
          aria-label="Scope"
          class="flex gap-[2px]"
        >
          <button
            v-for="s in SCOPES"
            :key="s.value"
            type="button"
            role="tab"
            :aria-selected="filter === s.value"
            class="border px-3 py-[5px] text-[12.5px]"
            :class="filter === s.value ? 'border-line bg-raised text-strong' : 'border-transparent text-toned hover:text-strong'"
            @click="filter = s.value"
          >
            {{ s.label }}
          </button>
        </div>
        <span class="flex-1" />
        <label class="flex h-8 items-center gap-2 border border-line px-2.5 text-muted focus-within:border-accent">
          <HIcon
            name="search"
            :size="13"
          />
          <input
            v-model="search"
            placeholder="Search as an agent would"
            aria-label="Search the knowledge base"
            class="w-[220px] bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
          >
        </label>
      </div>

      <div
        v-if="hits"
        class="flex flex-col gap-2"
      >
        <p
          v-if="!hits.length"
          class="m-0 text-[12.5px] text-muted"
        >
          Nothing matches.
        </p>
        <button
          v-for="(h, i) in hits"
          :key="`${h.documentId}:${i}`"
          type="button"
          class="flex flex-col gap-1 border border-line p-2.5 text-left hover:border-line-strong"
          @click="openDocument(h.documentId)"
        >
          <span class="flex items-center gap-2 text-[12.5px] text-strong">{{ h.title }}<span class="text-[11px] text-muted">{{ scopeLabel(h.scope) }}</span></span>
          <span class="line-clamp-4 whitespace-pre-wrap text-[12px] text-toned">{{ h.snippet }}</span>
        </button>
      </div>

      <div
        v-else
        class="flex flex-col"
      >
        <p
          v-if="loaded && !documents.length"
          class="m-0 text-[12.5px] text-muted"
        >
          No documents yet.
        </p>
        <button
          v-for="d in documents"
          :key="d.id"
          type="button"
          class="flex items-center gap-3 border-b border-line px-1 py-2.5 text-left hover:bg-hover"
          @click="openDocument(d.id)"
        >
          <HIcon
            name="page"
            class="shrink-0 text-muted"
          />
          <span class="min-w-0 flex-1 truncate text-[13px] text-strong">{{ d.title }}</span>
          <span class="w-[80px] shrink-0 text-[11.5px] text-muted">{{ scopeLabel(d.scope) }}</span>
          <span class="w-[70px] shrink-0 text-[11.5px] text-muted max-sm:hidden">{{ formatSize(d.size) }}</span>
          <span class="w-[90px] shrink-0 text-[11.5px] text-muted">{{ timeAgo(d.updatedAt) }}</span>
        </button>
      </div>
    </div>
  </section>
</template>
