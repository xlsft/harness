<script setup lang="ts">
// Artifacts: what sessions published, in a grid or a list (`view`), with one open in ArtifactViewer (?id=). The server says
// what the user may see: their own, private until they share it, and what others shared on this server.
import { ArtifactFilter, ArtifactVisibility, ARTIFACT_VIEW, errorText, formatSize, timeAgo, type Artifact } from '~/lib/library/types'

const props = defineProps<{ view: 'grid' | 'list' }>()
const emit = defineEmits<{ 'update:view': [view: 'grid' | 'list'] }>()

const store = useSessionStore()
const route = useRoute()
const router = useRouter()

const FILTERS = [
  { label: 'All', value: ArtifactFilter.ALL },
  { label: 'Yours', value: ArtifactFilter.MINE },
  { label: 'Shared', value: ArtifactFilter.SHARED }
]
const filter = ref(ArtifactFilter.ALL)
const query = ref('')
const items = ref<Artifact[]>([])
const loaded = ref(false)
const error = ref('')

const openId = computed(() => (typeof route.query.id === 'string' ? route.query.id : ''))

function open(id: string) {
  router.push({ query: { ...route.query, id } })
}
function close() {
  router.replace({ query: { ...route.query, id: undefined } })
}

let seq = 0
async function load() {
  const api = store.transport?.library()
  if (!api) return
  const n = ++seq
  try {
    const list = await api.listArtifacts({ filter: filter.value, query: query.value })
    if (n !== seq) return
    items.value = list
    error.value = ''
  } catch (e) {
    if (n === seq) error.value = errorText(e)
  } finally {
    if (n === seq) loaded.value = true
  }
}
onMounted(load)
watch(() => store.transport, load)
watch(filter, load)
let timer: ReturnType<typeof setTimeout> | undefined
watch(query, () => {
  clearTimeout(timer)
  timer = setTimeout(load, 200)
})
onBeforeUnmount(() => clearTimeout(timer))

function look(a: Artifact) {
  return ARTIFACT_VIEW[a.kind]
}
const isShared = (a: Artifact) => a.visibility === ArtifactVisibility.SHARED
const subtitle = (a: Artifact) => {
  const who = a.mine ? '' : `${a.ownerName || 'Someone else'} · `
  return `${who}${timeAgo(a.updatedAt)}`
}

function onDeleted() {
  close()
  load()
}
</script>

<template>
  <ArtifactViewer
    v-if="openId"
    :id="openId"
    class="pt-11"
    @close="close"
    @changed="load"
    @deleted="onDeleted"
  />
  <section
    v-else
    aria-label="Artifacts"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[1080px] flex-col gap-[22px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <h1 class="m-0 font-display text-[26px] font-bold text-strong">
        Artifacts
      </h1>

      <div class="flex flex-wrap items-center gap-2">
        <div
          role="tablist"
          aria-label="Filter"
          class="flex gap-[2px]"
        >
          <button
            v-for="f in FILTERS"
            :key="f.value"
            type="button"
            role="tab"
            :aria-selected="filter === f.value"
            class="border px-3 py-[5px] text-[12.5px]"
            :class="filter === f.value ? 'border-line bg-raised text-strong' : 'border-transparent text-toned hover:text-strong'"
            @click="filter = f.value"
          >
            {{ f.label }}
          </button>
        </div>
        <span class="flex-1" />
        <label class="flex h-8 items-center gap-2 border border-line px-2.5 text-muted focus-within:border-accent">
          <HIcon
            name="search"
            :size="13"
          />
          <input
            v-model="query"
            placeholder="Search titles"
            aria-label="Search artifacts"
            class="w-[180px] bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
          >
        </label>
        <button
          type="button"
          :aria-label="props.view === 'grid' ? 'Show as list' : 'Show as grid'"
          :title="props.view === 'grid' ? 'Show as list' : 'Show as grid'"
          class="flex size-8 items-center justify-center text-toned hover:text-strong"
          @click="emit('update:view', props.view === 'grid' ? 'list' : 'grid')"
        >
          <HIcon
            :name="props.view === 'grid' ? 'list' : 'grid'"
            :size="16"
          />
        </button>
      </div>

      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>
      <p
        v-else-if="loaded && !items.length"
        class="m-0 text-[12.5px] text-muted"
      >
        {{ query || filter !== ArtifactFilter.ALL ? 'Nothing matches.' : 'Nothing published yet. Ask an agent to publish a report or a page as an artifact and it stays here after the session, private until you share it.' }}
      </p>

      <div
        v-if="props.view === 'grid'"
        class="grid grid-cols-3 gap-3.5 max-md:grid-cols-2 max-sm:grid-cols-1"
      >
        <button
          v-for="a in items"
          :key="a.id"
          type="button"
          class="flex flex-col border border-line bg-surface text-left text-toned hover:border-line-strong"
          @click="open(a.id)"
        >
          <span
            class="flex h-[110px] w-full items-center justify-center border-b border-line"
            :style="{ background: look(a).tint, color: look(a).color }"
          >
            <HIcon
              :name="look(a).icon"
              :size="34"
            />
          </span>
          <span class="flex w-full items-center gap-3 px-3.5 py-3">
            <span class="flex min-w-0 flex-1 flex-col gap-[2px]">
              <span class="truncate text-[13px] text-strong">{{ a.title }}</span>
              <span class="truncate text-[11.5px] text-muted">{{ look(a).label }} · {{ subtitle(a) }}</span>
            </span>
            <HIcon
              v-if="isShared(a)"
              name="globe-share"
              class="text-toned"
              aria-label="Shared on this server"
            />
          </span>
        </button>
      </div>

      <div
        v-else
        class="flex flex-col"
      >
        <button
          v-for="a in items"
          :key="a.id"
          type="button"
          class="flex items-center gap-3 border-b border-line px-1 py-2.5 text-left text-toned hover:bg-hover"
          @click="open(a.id)"
        >
          <span
            class="flex size-8 shrink-0 items-center justify-center"
            :style="{ background: look(a).tint, color: look(a).color }"
          >
            <HIcon :name="look(a).icon" />
          </span>
          <span class="min-w-0 flex-1 truncate text-[13px] text-strong">{{ a.title }}</span>
          <span class="w-[70px] shrink-0 text-[11.5px] text-muted max-sm:hidden">{{ formatSize(a.current?.ref?.size ?? 0n) }}</span>
          <span class="w-[150px] shrink-0 truncate text-[11.5px] text-muted">{{ subtitle(a) }}</span>
          <HIcon
            v-if="isShared(a)"
            name="globe-share"
            class="shrink-0 text-toned"
            aria-label="Shared on this server"
          />
        </button>
      </div>
    </div>
  </section>
</template>
