<script setup lang="ts">
// One artifact: its title, versions, sharing, and the file itself. A page runs in a frame sandboxed with scripts only, so
// it has an opaque origin and no way to the app; the address's answer says the same, so a link pasted elsewhere is as
// safe as the frame. A PDF needs the browser's viewer, which a sandboxed answer turns away, so it comes through a blob.
import { ArtifactKind, ArtifactVisibility, ARTIFACT_VIEW, errorText, formatSize, timeAgo, type Artifact, type ArtifactVersion } from '~/lib/library/types'

const props = defineProps<{ id: string }>()
const emit = defineEmits<{ close: [], changed: [], deleted: [] }>()

const store = useSessionStore()
const api = computed(() => store.transport?.library())

const artifact = ref<Artifact | null>(null)
const versions = ref<ArtifactVersion[]>([])
const version = ref(0)
const text = ref('')
const url = ref('')
const blobUrl = ref('')
const error = ref('')
const confirming = ref(false)
const renaming = ref(false)
const draftTitle = ref('')

const kind = computed(() => artifact.value?.kind ?? ArtifactKind.UNSPECIFIED)
const view = computed(() => ARTIFACT_VIEW[kind.value])
const shared = computed(() => artifact.value?.visibility === ArtifactVisibility.SHARED)
const versionOptions = computed(() => versions.value.map(v => ({ value: String(v.number), label: `Version ${v.number}`, hint: timeAgo(v.createdAt) })))

function clearBlob() {
  if (blobUrl.value) URL.revokeObjectURL(blobUrl.value)
  blobUrl.value = ''
}
onBeforeUnmount(clearBlob)

async function load() {
  const a = api.value
  if (!a) return
  error.value = ''
  clearBlob()
  url.value = ''
  try {
    const res = await a.getArtifact(props.id, version.value)
    artifact.value = res.artifact ?? null
    versions.value = res.versions
    text.value = res.text
    const k = artifact.value?.kind
    if (k === ArtifactKind.PAGE || k === ArtifactKind.IMAGE || k === ArtifactKind.PDF) {
      url.value = await a.artifactUrl(props.id, { version: version.value })
      if (!url.value) error.value = 'Artifacts live on the server, and this client is not connected to one.'
    }
    if (k === ArtifactKind.PDF && url.value) {
      const res = await fetch(url.value)
      if (!res.ok) throw new Error(`The file could not be read (${res.status})`)
      blobUrl.value = URL.createObjectURL(new Blob([await res.arrayBuffer()], { type: 'application/pdf' }))
    }
  } catch (e) {
    error.value = errorText(e)
  }
}
watch(() => props.id, () => {
  version.value = 0
  artifact.value = null
  load()
}, { immediate: true })

const shownVersion = computed({
  get: () => String(version.value || artifact.value?.current?.number || ''),
  set: (v) => {
    version.value = Number(v) === artifact.value?.current?.number ? 0 : Number(v)
    load()
  }
})

async function act(fn: () => Promise<unknown>) {
  error.value = ''
  try {
    await fn()
  } catch (e) {
    error.value = errorText(e)
  }
}

const toggleShared = () => act(async () => {
  artifact.value = await api.value!.setArtifactShared(props.id, !shared.value)
  emit('changed')
})

function startRename() {
  draftTitle.value = artifact.value?.title ?? ''
  renaming.value = true
}
const saveTitle = () => act(async () => {
  artifact.value = await api.value!.renameArtifact(props.id, draftTitle.value)
  renaming.value = false
  emit('changed')
})

const download = () => act(async () => {
  const href = await api.value!.artifactUrl(props.id, { version: version.value, download: true })
  if (!href) return
  const a = document.createElement('a')
  a.href = href
  a.download = artifact.value?.current?.ref?.name || artifact.value?.title || 'artifact'
  a.rel = 'noopener'
  a.click()
})

const remove = () => act(async () => {
  if (!confirming.value) {
    confirming.value = true
    return
  }
  await api.value!.deleteArtifact(props.id)
  emit('deleted')
})
</script>

<template>
  <section
    aria-label="Artifact"
    class="flex h-full min-h-0 flex-col bg-default"
  >
    <div class="flex min-h-[44px] shrink-0 flex-wrap items-center gap-2 border-b border-line px-3.5 py-1.5">
      <PaneBtn
        icon="arrow-left"
        label="Back"
        @click="emit('close')"
      />
      <span
        v-if="artifact"
        class="flex size-6 shrink-0 items-center justify-center"
        :style="{ background: view.tint, color: view.color }"
      >
        <HIcon
          :name="view.icon"
          :size="14"
        />
      </span>
      <form
        v-if="renaming"
        class="flex min-w-0 flex-1 items-center gap-1.5"
        @submit.prevent="saveTitle"
      >
        <input
          v-model="draftTitle"
          aria-label="Title"
          class="min-w-0 flex-1 border border-line bg-transparent px-2 py-0.5 text-[13px] text-strong outline-none focus:border-accent"
        >
        <HBtn
          size="sm"
          variant="primary"
          type="submit"
        >
          Save
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          @click="renaming = false"
        >
          Cancel
        </HBtn>
      </form>
      <span
        v-else
        class="min-w-0 flex-1 truncate font-display text-[13.5px] text-strong"
      >
        {{ artifact?.title ?? 'Artifact' }}
        <span
          v-if="artifact && !artifact.mine"
          class="ml-1 text-[11.5px] text-muted"
        >by {{ artifact.ownerName || 'someone else' }}</span>
      </span>
      <HSelect
        v-if="versions.length > 1"
        v-model="shownVersion"
        class="w-[150px]"
        :options="versionOptions"
        aria-label="Version"
      />
      <template v-if="artifact?.mine && !renaming">
        <HBtn
          size="sm"
          @click="startRename"
        >
          Rename
        </HBtn>
        <label class="flex items-center gap-1.5 text-[12px] text-toned">
          <HToggle
            :model-value="shared"
            label="Share with everyone on this server"
            @update:model-value="toggleShared"
          />
          {{ shared ? 'Shared on this server' : 'Private' }}
        </label>
      </template>
      <HBtn
        v-if="artifact"
        size="sm"
        @click="download"
      >
        Download
      </HBtn>
      <HBtn
        v-if="artifact?.mine"
        size="sm"
        tone="danger"
        @click="remove"
      >
        {{ confirming ? 'Delete for good' : 'Delete' }}
      </HBtn>
    </div>

    <p
      v-if="error"
      role="alert"
      class="m-0 border-b border-line px-3.5 py-1.5 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="!artifact"
      class="flex flex-1 items-center justify-center text-[12.5px] text-muted"
    >
      {{ error ? '' : 'Loading' }}
    </div>
    <iframe
      v-else-if="kind === ArtifactKind.PAGE && url"
      :key="url"
      :src="url"
      :title="artifact.title"
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      class="min-h-0 w-full flex-1 border-0 bg-white"
    />
    <div
      v-else-if="kind === ArtifactKind.IMAGE && url"
      class="flex min-h-0 flex-1 items-start justify-center overflow-auto p-4"
    >
      <img
        :src="url"
        :alt="artifact.title"
        class="max-w-full"
      >
    </div>
    <iframe
      v-else-if="kind === ArtifactKind.PDF && blobUrl"
      :src="blobUrl"
      :title="artifact.title"
      class="min-h-0 w-full flex-1 border-0"
    />
    <div
      v-else-if="kind === ArtifactKind.MARKDOWN && text"
      class="min-h-0 flex-1 overflow-auto px-6 py-4"
    >
      <MarkdownBlock :text="text" />
    </div>
    <pre
      v-else-if="kind === ArtifactKind.TEXT && text"
      class="m-0 min-h-0 flex-1 overflow-auto p-4 font-mono text-[12px] text-toned"
    >{{ text }}</pre>
    <div
      v-else
      class="flex flex-1 flex-col items-center justify-center gap-2 text-[12.5px] text-muted"
    >
      <span>{{ artifact.current?.ref?.name }} · {{ formatSize(artifact.current?.ref?.size ?? 0n) }}</span>
      <span>This file is not shown here. Download it to open it.</span>
    </div>
  </section>
</template>
