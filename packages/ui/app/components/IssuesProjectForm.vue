<script setup lang="ts">
// A project's metadata, with whom it is shared and at what level, and the folder of this device it is linked to. Without `project`,
// a new one, internal or mapped onto a provider's project. The server enforces the levels and checks the values.
import { errorText } from '~/lib/library/types'
import { BoardScope, LEVELS, ProjectLevel, ProjectScope, ProjectShareSubject, TrackerConnectionScope, type Project, type ProviderProject, type ShareInit, type TrackerConnection } from '~/lib/issues/types'

const props = defineProps<{ project?: Project }>()
const emit = defineEmits<{ close: [], changed: [], created: [id: string] }>()

const store = useSessionStore()
const api = () => store.transport?.issues()
const error = ref('')
const saved = ref('')
const isAdmin = ref(false)
const confirming = ref(false)

const form = reactive({
  ticker: props.project?.local?.ticker ?? '',
  name: props.project?.local?.name ?? '',
  description: props.project?.local?.description ?? '',
  logo: props.project?.local?.logo ?? '',
  color: props.project?.local?.color ?? '',
  global: false,
  connection: '',
  key: ''
})

const connections = ref<TrackerConnection[]>([])
const providerProjects = ref<ProviderProject[]>([])
const picked = computed(() => providerProjects.value.find(p => p.key === form.key))
const defaults = computed(() => props.project?.provider ?? picked.value?.metadata)

const shares = ref<ShareInit[]>((props.project?.shares ?? []).map(s => ({ subject: s.subject, subjectId: s.subjectId, level: s.level })))
const shareName = (s: ShareInit) => props.project?.shares.find(x => x.subject === s.subject && x.subjectId === s.subjectId)?.subjectName ?? s.subjectId
const newShare = reactive({ subject: String(ProjectShareSubject.USER), subjectId: '', level: String(ProjectLevel.VIEW) })
const SUBJECTS = [
  { value: String(ProjectShareSubject.USER), label: tt('User') },
  { value: String(ProjectShareSubject.GROUP), label: tt('Group') },
  { value: String(ProjectShareSubject.ROLE), label: tt('Role') },
  { value: String(ProjectShareSubject.EVERYONE), label: tt('Everyone') }
]
const levelOptions = LEVELS.map(l => ({ value: String(l.value), label: l.label }))
const subjectLabel = (s: ShareInit) => SUBJECTS.find(x => Number(x.value) === s.subject)?.label ?? ''

const folder = ref('')
const linkedFolder = ref('')
const canManage = computed(() => !props.project || props.project.level === ProjectLevel.MANAGE)

onMounted(async () => {
  const a = api()
  if (!a) return
  try {
    isAdmin.value = await store.transport!.users().isAdmin().catch(() => false)
    connections.value = (await a.listConnections()).connections.filter(c => c.enabled)
    if (props.project) {
      linkedFolder.value = (await a.listFolderLinks()).find(l => l.projectId === props.project!.id)?.folder ?? ''
      folder.value = linkedFolder.value
    }
  } catch (e) {
    error.value = errorText(e)
  }
})

const refOf = (id: string) => {
  const c = connections.value.find(x => x.id === id)
  return c ? { scope: c.scope, id: c.id } : undefined
}

watch(() => form.connection, async (id) => {
  providerProjects.value = []
  form.key = ''
  const ref = refOf(id)
  if (!ref) return
  try {
    providerProjects.value = (await api()?.listProviderProjects(ref)) ?? []
  } catch (e) {
    error.value = errorText(e)
  }
})

async function save() {
  const a = api()
  if (!a) return
  error.value = ''
  saved.value = ''
  try {
    const metadata = { ticker: form.ticker.trim().toUpperCase(), name: form.name.trim(), description: form.description, logo: form.logo.trim(), color: form.color.trim() }
    if (props.project) {
      await a.updateProject(props.project.id, metadata)
      await a.setShares(props.project.id, shares.value)
      const next = folder.value.trim()
      if (linkedFolder.value && linkedFolder.value !== next) await a.setFolderLink(linkedFolder.value, '')
      if (next) await a.setFolderLink(next, props.project.id)
      linkedFolder.value = next
      saved.value = tt('Saved')
      emit('changed')
    } else {
      const ref = refOf(form.connection)
      const p = await a.createProject({ scope: form.global ? ProjectScope.GLOBAL : ProjectScope.PERSONAL, metadata, connection: ref, externalKey: ref ? form.key : '', provider: ref ? picked.value?.metadata : undefined })
      await a.createBoard({ projectId: p.id, scope: form.global ? BoardScope.SHARED : BoardScope.PERSONAL, name: tt('Board') })
      emit('created', p.id)
    }
  } catch (e) {
    error.value = errorText(e)
  }
}

function addShare() {
  const subject = Number(newShare.subject) as ProjectShareSubject
  const subjectId = subject === ProjectShareSubject.EVERYONE ? '' : newShare.subjectId.trim()
  if (subject !== ProjectShareSubject.EVERYONE && !subjectId) return
  shares.value = [...shares.value.filter(s => !(s.subject === subject && s.subjectId === subjectId)), { subject, subjectId, level: Number(newShare.level) as ProjectLevel }]
  newShare.subjectId = ''
}

async function remove() {
  if (!confirming.value) {
    confirming.value = true
    return
  }
  try {
    await api()?.deleteProject(props.project!.id)
    emit('changed')
    emit('close')
  } catch (e) {
    error.value = errorText(e)
  }
}

const connectionOptions = computed(() => [{ value: '', label: tt('Internal') }, ...connections.value.map(c => ({ value: c.id, label: c.scope === TrackerConnectionScope.DEVICE ? tt('{0} (this device)', [c.name]) : c.name }))])
const keyOptions = computed(() => providerProjects.value.map(p => ({ value: p.key, label: `${p.key} · ${p.metadata?.name}` })))
const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <form
    class="flex flex-col gap-2.5 border border-line p-3.5"
    @submit.prevent="save"
  >
    <p
      v-if="error"
      role="alert"
      class="m-0 text-[12.5px] text-bad"
    >
      {{ error }}
    </p>
    <div
      v-if="!project"
      class="flex flex-wrap items-center gap-2 text-[12.5px] text-toned"
    >
      <span>{{ tt('Issues from') }}</span>
      <HSelect
        v-model="form.connection"
        class="w-[200px]"
        :options="connectionOptions"
        :aria-label="tt('Issues from')"
      />
      <HSelect
        v-if="form.connection"
        v-model="form.key"
        class="w-[220px]"
        :options="keyOptions"
        :aria-label="tt('The provider\'s project')"
      />
      <label
        v-if="isAdmin"
        class="flex items-center gap-1.5"
      >
        <HToggle
          v-model="form.global"
          :label="tt('Global project')"
        />
        {{ tt('Global') }}
      </label>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <input
        v-model="form.ticker"
        :placeholder="defaults?.ticker || tt('Ticker, such as ABC')"
        :aria-label="tt('Ticker')"
        :readonly="!canManage"
        :class="[inputClass, 'w-[130px]']"
      >
      <input
        v-model="form.name"
        :placeholder="defaults?.name || tt('Name')"
        :aria-label="tt('Name')"
        :readonly="!canManage"
        :class="[inputClass, 'min-w-[180px] flex-1']"
      >
      <input
        v-model="form.logo"
        :placeholder="defaults?.logo || tt('Logo: an image URL or an emoji')"
        :aria-label="tt('Logo')"
        :readonly="!canManage"
        :class="[inputClass, 'w-[180px]']"
      >
      <input
        v-model="form.color"
        :placeholder="tt('Color, #rrggbb')"
        :aria-label="tt('Color')"
        :readonly="!canManage"
        :class="[inputClass, 'w-[110px]']"
      >
    </div>
    <textarea
      v-model="form.description"
      :placeholder="defaults?.description || tt('Description')"
      :aria-label="tt('Description')"
      :readonly="!canManage"
      rows="2"
      class="resize-y border border-line bg-transparent p-2 text-[12.5px] text-strong outline-none focus:border-accent"
    />
    <p
      v-if="defaults?.name"
      class="m-0 text-[11.5px] text-muted"
    >
      {{ tt('What you leave empty comes from the provider.') }}
    </p>

    <template v-if="project">
      <label class="flex flex-wrap items-center gap-2 text-[12.5px] text-toned">
        {{ tt('Folder on this device') }}
        <input
          v-model="folder"
          :placeholder="tt('/path/to/the/repository')"
          :class="[inputClass, 'min-w-[240px] flex-1 font-mono']"
        >
      </label>
      <p class="m-0 text-[11.5px] text-muted">
        {{ tt('Sessions opened in this folder know their project, and an issue\'s session starts here. The link stays on this device.') }}
      </p>

      <template v-if="canManage">
        <h3 class="m-0 text-[12px] font-medium text-muted">
          {{ tt('Shared with') }}
        </h3>
        <p
          v-if="!shares.length"
          class="m-0 text-[12px] text-muted"
        >
          {{ project.scope === ProjectScope.PERSONAL ? tt('Only you.') : tt('Only admins.') }}
        </p>
        <div
          v-for="s in shares"
          :key="`${s.subject}:${s.subjectId}`"
          class="flex items-center gap-2 text-[12.5px] text-toned"
        >
          <span class="w-[80px] text-muted">{{ subjectLabel(s) }}</span>
          <span class="min-w-0 flex-1 truncate">{{ shareName(s) }}</span>
          <HSelect
            :model-value="String(s.level)"
            class="w-[120px]"
            :options="levelOptions"
            :aria-label="tt('Level')"
            @update:model-value="s.level = Number($event)"
          />
          <button
            type="button"
            class="text-muted hover:text-strong"
            :aria-label="tt('Stop sharing with {0}', [shareName(s)])"
            @click="shares = shares.filter(x => x !== s)"
          >
            ×
          </button>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <HSelect
            v-model="newShare.subject"
            class="w-[110px]"
            :options="SUBJECTS"
            :aria-label="tt('Share with')"
          />
          <input
            v-if="Number(newShare.subject) !== ProjectShareSubject.EVERYONE"
            v-model="newShare.subjectId"
            :placeholder="Number(newShare.subject) === ProjectShareSubject.ROLE ? 'admin, user' : Number(newShare.subject) === ProjectShareSubject.GROUP ? tt('Name of the group') : tt('Email')"
            :aria-label="tt('Who')"
            :class="[inputClass, 'w-[180px]']"
          >
          <HSelect
            v-model="newShare.level"
            class="w-[120px]"
            :options="levelOptions"
            :aria-label="tt('Level')"
          />
          <HBtn
            size="sm"
            @click="addShare"
          >
            {{ tt('Add') }}
          </HBtn>
        </div>
        <p class="m-0 text-[11.5px] text-muted">
          {{ tt('Sharing covers the project\'s shared boards. Issues on a personal board stay yours.') }}
        </p>
      </template>
    </template>

    <div class="flex items-center gap-2">
      <HBtn
        size="md"
        variant="primary"
        type="submit"
      >
        {{ project ? tt('Save') : tt('Create') }}
      </HBtn>
      <HBtn
        size="md"
        variant="ghost"
        @click="emit('close')"
      >
        {{ tt('Close') }}
      </HBtn>
      <span class="text-[12px] text-muted">{{ saved }}</span>
      <span class="flex-1" />
      <HBtn
        v-if="project && canManage"
        size="md"
        tone="danger"
        @click="remove"
      >
        {{ confirming ? tt('Delete with all its issues') : tt('Delete') }}
      </HBtn>
    </div>
  </form>
</template>
