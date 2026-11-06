<script setup lang="ts">
// Artifact pane: what this session published, without leaving it. One opens in the same viewer as the Artifacts screen.
import { usePanes } from '~/composables/usePanes'
import { ArtifactFilter, ARTIFACT_VIEW, errorText, timeAgo, type Artifact } from '~/lib/library/types'
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState, menu: string }>()

const { closePane } = usePanes()
const store = useSessionStore()
const items = ref<Artifact[]>([])
const selected = ref('')
const loaded = ref(false)
const error = ref('')

async function load() {
  const api = store.transport?.library()
  if (!api) return
  try {
    items.value = await api.listArtifacts({ filter: ArtifactFilter.MINE, sessionId: props.session.meta.id })
    error.value = ''
    if (items.value.length === 1 && !selected.value) selected.value = items.value[0]!.id
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loaded.value = true
  }
}
onMounted(load)
watch(() => props.session.meta.id, () => {
  selected.value = ''
  load()
})
</script>

<template>
  <ArtifactViewer
    v-if="selected"
    :id="selected"
    @close="selected = ''"
    @changed="load"
    @deleted="selected = ''; load()"
  />
  <section
    v-else
    :aria-label="tt('Artifacts of this session')"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="min-w-0 flex-1 truncate font-display text-[13px] text-strong">{{ tt('Artifacts') }}</span>
      <PaneFullBtn />
      <PaneBtn
        icon="close"
        :label="tt('Close')"
        @click="closePane('artifact')"
      />
    </div>
    <p
      v-if="error"
      role="alert"
      class="m-0 px-3.5 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <p
      v-else-if="loaded && !items.length"
      class="m-0 px-3.5 py-3 text-[12.5px] text-muted"
    >
      {{ tt('This session has published nothing yet.') }}
    </p>
    <div class="min-h-0 flex-1 overflow-y-auto">
      <button
        v-for="a in items"
        :key="a.id"
        type="button"
        class="flex w-full items-center gap-2.5 border-b border-line px-3.5 py-2 text-left hover:bg-hover"
        @click="selected = a.id"
      >
        <span
          class="flex size-7 shrink-0 items-center justify-center"
          :style="{ background: ARTIFACT_VIEW[a.kind].tint, color: ARTIFACT_VIEW[a.kind].color }"
        >
          <HIcon :name="ARTIFACT_VIEW[a.kind].icon" />
        </span>
        <span class="flex min-w-0 flex-1 flex-col">
          <span class="truncate text-[13px] text-strong">{{ a.title }}</span>
          <span class="text-[11.5px] text-muted">{{ timeAgo(a.updatedAt) }}</span>
        </span>
      </button>
    </div>
  </section>
</template>
