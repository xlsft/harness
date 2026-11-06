<script setup lang="ts">
// Settings > Memory: what the agent keeps per project, to read, change and delete, and the switch that turns it off. The
// client rebuilds a project's index from its files; nothing here writes the index.
import { errorText, type AutoMemoryProject } from '~/lib/library/types'

const store = useSessionStore()
const enabled = ref(true)
const projects = ref<AutoMemoryProject[]>([])
const projectKey = ref('')
const loaded = ref(false)
const error = ref('')

const api = () => store.transport?.library()
const project = computed(() => projects.value.find(p => p.projectKey === projectKey.value) ?? null)
const projectOptions = computed(() => projects.value.map(p => ({ value: p.projectKey, label: p.projectKey, hint: `${p.files.length} ${p.files.length === 1 ? 'note' : 'notes'}` })))

// The note being edited: an existing file, or a new one with a name to choose.
const editing = ref<{ name: string, isNew: boolean, content: string } | null>(null)
const confirming = ref(false)

const NEW_NOTE = '---\nname: \ndescription: \ntype: project\n---\n\n'

onMounted(async () => {
  if (!store.transport) return
  try {
    enabled.value = (await store.transport.getUserPreferences()).autoMemoryEnabled
    const cwd = store.current && !store.current.meta.remote && !store.current.meta.noFolder ? store.current.meta.cwd : ''
    const list = await api()?.listMemory(cwd) ?? []
    projects.value = list
    projectKey.value = list.find(p => p.current)?.projectKey ?? list[0]?.projectKey ?? ''
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loaded.value = true
  }
})

async function setEnabled(v: boolean) {
  if (!store.transport) return
  const prev = enabled.value
  enabled.value = v
  try {
    enabled.value = (await store.transport.updateUserPreferences({ autoMemoryEnabled: v })).autoMemoryEnabled
    error.value = ''
  } catch (e) {
    enabled.value = prev
    error.value = errorText(e)
  }
}

function apply(p: AutoMemoryProject | undefined) {
  if (!p) return
  projects.value = projects.value.map(x => (x.projectKey === p.projectKey ? { ...p, current: x.current } : x))
}

function edit(name: string, content: string) {
  confirming.value = false
  editing.value = { name, isNew: false, content }
}

async function save() {
  const e = editing.value
  if (!e || !project.value) return
  try {
    apply(await api()?.saveMemory(project.value.projectKey, e.name, e.content))
    editing.value = null
    error.value = ''
  } catch (err) {
    error.value = errorText(err)
  }
}

async function remove() {
  const e = editing.value
  if (!e || !project.value) return
  if (!confirming.value) {
    confirming.value = true
    return
  }
  try {
    apply(await api()?.deleteMemory(project.value.projectKey, e.name))
    editing.value = null
    confirming.value = false
    error.value = ''
  } catch (err) {
    error.value = errorText(err)
  }
}
</script>

<template>
  <section
    aria-label="Memory"
    class="flex flex-col"
  >
    <h2 class="set-h">
      Memory
    </h2>
    <p class="set-sub">
      The agent keeps notes per project for later sessions, and a new session starts with their index. They are stored on this machine and on your account on the server, and nobody else can see them.
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <label class="mb-4 flex items-center gap-3 text-[13px] text-strong">
      <HToggle
        :model-value="enabled"
        label="Auto memory"
        @update:model-value="setEnabled"
      />
      <span class="flex flex-col">
        Auto memory
        <span class="text-[12px] text-muted">Off, the agent neither writes notes nor sees them. What is stored stays here until you delete it.</span>
      </span>
    </label>

    <p
      v-if="loaded && !projects.length"
      class="m-0 text-[12.5px] text-muted"
    >
      No notes yet.
    </p>
    <template v-if="projects.length">
      <div class="mb-3 flex items-center gap-3 text-[12.5px] text-muted">
        <span>Project</span>
        <HSelect
          v-model="projectKey"
          class="min-w-[260px] flex-1"
          :options="projectOptions"
          aria-label="Project"
          @update:model-value="editing = null"
        />
        <HBtn
          size="sm"
          :disabled="!project"
          @click="editing = { name: '', isNew: true, content: NEW_NOTE }; confirming = false"
        >
          New note
        </HBtn>
      </div>

      <p
        v-if="project && !project.files.length"
        class="m-0 text-[12.5px] text-muted"
      >
        No notes for this project yet.
      </p>
      <div class="flex flex-col">
        <button
          v-for="f in project?.files ?? []"
          :key="f.name"
          type="button"
          class="flex items-baseline gap-3 border-b border-line px-1 py-2 text-left hover:bg-hover"
          @click="edit(f.name, f.content)"
        >
          <span class="shrink-0 font-mono text-[12px] text-strong">{{ f.name }}</span>
          <span class="min-w-0 flex-1 truncate text-[12px] text-muted">{{ f.description }}</span>
        </button>
      </div>

      <form
        v-if="editing"
        class="mt-3 flex flex-col gap-2 border border-line p-3"
        @submit.prevent="save"
      >
        <input
          v-if="editing.isNew"
          v-model="editing.name"
          placeholder="name (letters, digits, _ and -)"
          aria-label="Note name"
          class="border border-line bg-transparent px-2 py-1 font-mono text-[12px] text-strong outline-none focus:border-accent"
        >
        <span
          v-else
          class="font-mono text-[12px] text-strong"
        >{{ editing.name }}</span>
        <textarea
          v-model="editing.content"
          aria-label="Note"
          rows="12"
          class="resize-y border border-line bg-transparent p-2 font-mono text-[12px] text-strong outline-none focus:border-accent"
        />
        <div class="flex items-center gap-2">
          <HBtn
            size="md"
            variant="primary"
            type="submit"
          >
            Save
          </HBtn>
          <HBtn
            size="md"
            variant="ghost"
            @click="editing = null"
          >
            Cancel
          </HBtn>
          <span class="flex-1" />
          <HBtn
            v-if="!editing.isNew"
            size="md"
            tone="danger"
            @click="remove"
          >
            {{ confirming ? 'Delete for good' : 'Delete' }}
          </HBtn>
        </div>
      </form>

      <details
        v-if="project?.index"
        class="mt-4 text-[12px] text-muted"
      >
        <summary class="cursor-pointer">
          The index a new session loads
        </summary>
        <pre class="m-0 mt-2 overflow-auto border border-line p-2 font-mono text-[12px] text-toned">{{ project.index }}</pre>
      </details>
    </template>
  </section>
</template>
