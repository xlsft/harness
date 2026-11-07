<script setup lang="ts">
// Knowledge: every source this user can read. Internal documents live on the server; documents of connected trackers are read live.
// Agents search them all with KnowledgeSearch. The server and the client enforce who sees what: `writable` and `editable` are their
// answers, and writing only appears where they would accept it.
import { openExternal } from '~/lib/desktop'
import { errorText, formatSize, KnowledgeSourceKind, Scope, timeAgo, type KnowledgeDocument, type KnowledgeHit, type KnowledgeSource, type KnowledgeWarning } from '~/lib/library/types'

const store = useSessionStore()
const sources = ref<KnowledgeSource[]>([])
const sourceId = ref('')
const documents = ref<KnowledgeDocument[]>([])
const crumbs = ref<{ id: string, title: string }[]>([])
const hasMore = ref(false)
const loaded = ref(false)
const error = ref('')

const search = ref('')
const hits = ref<KnowledgeHit[] | null>(null)
const warnings = ref<KnowledgeWarning[]>([])

const api = () => store.transport?.library()
const source = computed(() => sources.value.find(s => s.ref?.id === sourceId.value))
const isInternal = computed(() => source.value?.ref?.kind === KnowledgeSourceKind.INTERNAL)
const writableScopes = computed(() => sources.value.filter(s => s.writable && s.ref?.kind === KnowledgeSourceKind.INTERNAL))

const scopeText = (s: Scope | undefined) => (s === Scope.ORG ? tt('Global') : tt('Yours'))

async function loadSources() {
  const a = api()
  if (!a) return
  try {
    sources.value = await a.listSources()
    if (!sources.value.some(s => s.ref?.id === sourceId.value)) sourceId.value = sources.value[0]?.ref?.id ?? ''
    await browse()
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loaded.value = true
  }
}

async function browse() {
  const a = api()
  if (!a || !sourceId.value) return
  try {
    const r = await a.browseSource(sourceId.value, crumbs.value.at(-1)?.id ?? '')
    documents.value = r.documents
    hasMore.value = r.hasMore
    error.value = ''
  } catch (e) {
    documents.value = []
    error.value = errorText(e)
  }
}

function pick(id: string) {
  sourceId.value = id
  crumbs.value = []
  editing.value = null
  void browse()
}

onMounted(loadSources)
watch(() => store.transport, loadSources)

let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined
watch(search, (q) => {
  clearTimeout(timer)
  if (!q.trim()) {
    seq++
    hits.value = null
    warnings.value = []
    return
  }
  timer = setTimeout(async () => {
    const n = ++seq
    try {
      const res = await api()?.searchKnowledge(q)
      if (n === seq) {
        hits.value = res?.hits ?? []
        warnings.value = res?.warnings ?? []
      }
    } catch (e) {
      if (n === seq) error.value = errorText(e)
    }
  }, 250)
})
onBeforeUnmount(() => clearTimeout(timer))

// The reader and editor: a new document (id '') or an open one.
const editing = ref<{ id: string, title: string, content: string, scope: Scope, sourceName: string, editable: boolean, url: string, sourceLabel: string } | null>(null)
const saving = ref(false)
const confirming = ref(false)

function newDocument() {
  confirming.value = false
  const scope = source.value?.writable && isInternal.value ? source.value.ref!.scope : Scope.USER
  editing.value = { id: '', title: '', content: '', scope, sourceName: '', editable: true, url: '', sourceLabel: '' }
}

async function openDocument(id: string) {
  confirming.value = false
  try {
    const res = await api()?.readDocument(id)
    if (!res?.document) return
    const d = res.document
    editing.value = { id, title: d.title, content: res.content, scope: d.scope, sourceName: d.sourceName, editable: d.editable, url: d.url, sourceLabel: d.source?.name ?? '' }
  } catch (e) {
    error.value = errorText(e)
  }
}

function openChildren(d: KnowledgeDocument) {
  crumbs.value = [...crumbs.value, { id: d.id.split(':').slice(2).join(':'), title: d.title }]
  void browse()
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
    error.value = tt('{0} is not a text file. Knowledge takes text and Markdown.', [file.name])
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
    await loadSources()
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
    await loadSources()
  } catch (e) {
    error.value = errorText(e)
  }
}
</script>

<template>
  <section
    :aria-label="tt('Knowledge')"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[980px] flex-col gap-[18px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <div class="flex items-center gap-3">
        <h1 class="m-0 flex-1 font-display text-[26px] font-bold text-strong">
          {{ tt('Knowledge') }}
        </h1>
        <HBtn
          size="md"
          variant="primary"
          @click="newDocument"
        >
          {{ tt('Add a document') }}
        </HBtn>
      </div>
      <p class="m-0 text-[12.5px] text-muted">
        {{ tt('Agents search all of these with KnowledgeSearch, as reference material rather than instructions. Documents from a tracker are read live and never copied here. What is yours stays visible to you alone.') }}
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
          :placeholder="tt('Title')"
          :aria-label="tt('Title')"
          :readonly="!editing.editable"
          class="border border-line bg-transparent px-2.5 py-1.5 text-[13px] text-strong outline-none focus:border-accent"
        >
        <div
          v-if="editing.sourceLabel"
          class="flex flex-wrap items-center gap-2 text-[12px] text-muted"
        >
          <span>{{ tt('From {0}', [editing.sourceLabel]) }}</span>
          <button
            v-if="editing.url"
            type="button"
            class="text-accent hover:underline"
            @click="openExternal(editing.url)"
          >
            {{ tt('Open in the tracker') }}
          </button>
        </div>
        <div
          v-if="!editing.id"
          class="flex flex-wrap items-center gap-3 text-[12.5px] text-toned"
        >
          <template v-if="writableScopes.length > 1">
            <span>{{ tt('Visible to') }}</span>
            <div class="w-[150px] shrink-0">
              <HSelect
                :model-value="String(editing.scope)"
                :options="[{ value: String(Scope.USER), label: tt('Only you') }, { value: String(Scope.ORG), label: tt('Everyone') }]"
                :aria-label="tt('Visible to')"
                @update:model-value="editing!.scope = Number($event)"
              />
            </div>
          </template>
          <label class="cursor-pointer text-accent hover:underline">
            {{ tt('Load from a text file') }}
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
          :placeholder="tt('Text or Markdown')"
          :aria-label="tt('Content')"
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
            {{ tt('Save') }}
          </HBtn>
          <HBtn
            size="md"
            variant="ghost"
            @click="editing = null"
          >
            {{ editing.editable ? tt('Cancel') : tt('Close') }}
          </HBtn>
          <span class="flex-1" />
          <HBtn
            v-if="editing.id && editing.editable"
            size="md"
            tone="danger"
            @click="remove"
          >
            {{ confirming ? tt('Delete for good') : tt('Delete') }}
          </HBtn>
        </div>
      </form>

      <label class="flex h-8 items-center gap-2 border border-line px-2.5 text-muted focus-within:border-accent">
        <HIcon
          name="search"
          :size="13"
        />
        <input
          v-model="search"
          :placeholder="tt('Search every source, as an agent would')"
          :aria-label="tt('Search Knowledge')"
          class="min-w-0 flex-1 bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
        >
      </label>

      <div
        v-if="hits"
        class="flex flex-col gap-2"
      >
        <p
          v-for="w in warnings"
          :key="w.source?.id"
          role="status"
          class="m-0 text-[12px] text-bad"
        >
          {{ tt('{0} could not answer: {1}', [w.source?.name ?? '', w.message]) }}
        </p>
        <p
          v-if="!hits.length"
          class="m-0 text-[12.5px] text-muted"
        >
          {{ tt('Nothing matches.') }}
        </p>
        <button
          v-for="(h, i) in hits"
          :key="`${h.documentId}:${i}`"
          type="button"
          class="flex flex-col gap-1 border border-line p-2.5 text-left hover:border-line-strong"
          @click="openDocument(h.documentId)"
        >
          <span class="flex items-center gap-2 text-[12.5px] text-strong">{{ h.title }}<span class="text-[11px] text-muted">{{ h.source?.name }}</span></span>
          <span class="line-clamp-4 whitespace-pre-wrap text-[12px] text-toned">{{ h.snippet }}</span>
        </button>
      </div>

      <div
        v-else
        class="flex gap-5 max-sm:flex-col"
      >
        <nav
          :aria-label="tt('Sources')"
          class="flex w-[220px] shrink-0 flex-col gap-[2px] max-sm:w-full"
        >
          <button
            v-for="s in sources"
            :key="s.ref?.id"
            type="button"
            class="flex flex-col border px-2.5 py-1.5 text-left text-[12.5px]"
            :class="sourceId === s.ref?.id ? 'border-line bg-raised text-strong' : 'border-transparent text-toned hover:text-strong'"
            :aria-current="sourceId === s.ref?.id"
            @click="pick(s.ref?.id ?? '')"
          >
            <span class="truncate">{{ s.ref?.name }}</span>
            <span class="text-[11px] text-muted">{{ scopeText(s.ref?.scope) }}<template v-if="s.ref?.provider"> · {{ s.ref.provider }}</template><template v-else> · {{ s.documentCount }}</template></span>
          </button>
        </nav>
        <div class="flex min-w-0 flex-1 flex-col">
          <div
            v-if="crumbs.length"
            class="flex flex-wrap items-center gap-1.5 pb-2 text-[12px] text-muted"
          >
            <button
              type="button"
              class="text-accent hover:underline"
              @click="crumbs = []; browse()"
            >
              {{ source?.ref?.name }}
            </button>
            <template
              v-for="(c, i) in crumbs"
              :key="c.id"
            >
              <span>/</span>
              <button
                type="button"
                class="text-accent hover:underline"
                @click="crumbs = crumbs.slice(0, i + 1); browse()"
              >
                {{ c.title }}
              </button>
            </template>
          </div>
          <p
            v-if="loaded && !documents.length && !error"
            class="m-0 text-[12.5px] text-muted"
          >
            {{ tt('No documents here.') }}
          </p>
          <div
            v-for="d in documents"
            :key="d.id"
            class="flex items-center gap-3 border-b border-line px-1 hover:bg-hover"
          >
            <button
              type="button"
              class="flex min-w-0 flex-1 items-center gap-3 py-2.5 text-left"
              @click="openDocument(d.id)"
            >
              <HIcon
                name="page"
                class="shrink-0 text-muted"
              />
              <span class="min-w-0 flex-1 truncate text-[13px] text-strong">{{ d.title }}</span>
              <span
                v-if="isInternal"
                class="w-[70px] shrink-0 text-[11.5px] text-muted max-sm:hidden"
              >{{ formatSize(d.size) }}</span>
              <span class="w-[90px] shrink-0 text-[11.5px] text-muted">{{ timeAgo(d.updatedAt) }}</span>
            </button>
            <HBtn
              v-if="d.hasChildren"
              size="sm"
              variant="ghost"
              :aria-label="tt('Open what is under {0}', [d.title])"
              @click="openChildren(d)"
            >
              {{ tt('Inside') }}
            </HBtn>
          </div>
          <p
            v-if="hasMore"
            class="m-0 pt-2 text-[12px] text-muted"
          >
            {{ tt('There are more; search to find them.') }}
          </p>
        </div>
      </div>
    </div>
  </section>
</template>
