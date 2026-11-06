<script setup lang="ts">
// A board's settings: its name and its stages, each with an optional input and output per connection. A shared board offers only
// company connections, as the server would refuse the others. Queries are the provider's own and are tested from here.
import { errorText } from '~/lib/library/types'
import { BoardScope, stagesToInit, TrackerConnectionScope, type Board, type Project, type StageInit, type TrackerCapabilities, type TrackerConnection, type TrackerField, type TrackerIssue } from '~/lib/issues/types'

const props = defineProps<{ board: Board, project?: Project }>()
const emit = defineEmits<{ close: [], changed: [] }>()

const store = useSessionStore()
const api = () => store.transport?.issues()
const name = ref(props.board.name)
const stages = ref<StageInit[]>(stagesToInit(props.board.stages))
const connections = ref<TrackerConnection[]>([])
const providers = ref<TrackerCapabilities[]>([])
const error = ref('')
const confirming = ref(false)

onMounted(async () => {
  try {
    const r = await api()?.listConnections()
    providers.value = r?.providers ?? []
    connections.value = (r?.connections ?? []).filter(c => props.board.scope === BoardScope.PERSONAL || c.scope === TrackerConnectionScope.ADMIN)
  } catch (e) {
    error.value = errorText(e)
  }
})

const options = computed(() => connections.value.map(c => ({ value: `${c.scope}:${c.id}`, label: c.scope === TrackerConnectionScope.DEVICE ? tt('{0} (this device)', [c.name]) : c.name })))
const keyOf = (c: { scope: TrackerConnectionScope, id: string }) => `${c.scope}:${c.id}`
const parseRef = (v: string) => {
  const at = v.indexOf(':')
  return { scope: Number(v.slice(0, at)) as TrackerConnectionScope, id: v.slice(at + 1) }
}
const known = (c: { id: string }) => !c.id || connections.value.some(x => x.id === c.id)
const connectionName = (c: { scope: TrackerConnectionScope, id: string }) => connections.value.find(x => x.id === c.id)?.name ?? c.id
const queryExample = (c: { id: string }) => {
  const p = connections.value.find(x => x.id === c.id)
  return providers.value.find(x => x.provider === p?.provider)?.queryExample || tt('A query')
}

function addStage() {
  stages.value.push({ name: '', inputs: [], outputs: [] })
}

function moveStage(i: number, by: number) {
  const list = [...stages.value]
  const [s] = list.splice(i, 1)
  list.splice(i + by, 0, s!)
  stages.value = list
}

function firstConnection() {
  const c = connections.value[0]
  return c ? { scope: c.scope, id: c.id } : undefined
}

function addInput(s: StageInit) {
  const connection = firstConnection()
  if (connection) (s.inputs ??= []).push({ connection, query: '', intervalSeconds: 300 })
}

function addOutput(s: StageInit) {
  const connection = firstConnection()
  if (connection) (s.outputs ??= []).push({ connection, fields: [{ name: '', value: '' }], comment: '' })
}

// The Test button of an input: what it would pull in right now.
const results = ref<Record<string, { issues?: TrackerIssue[], error?: string }>>({})
async function testQuery(key: string, c: { scope: TrackerConnectionScope, id: string }, query: string) {
  try {
    results.value[key] = { issues: (await api()?.searchConnection(c, query)) ?? [] }
  } catch (e) {
    results.value[key] = { error: errorText(e) }
  }
}

// What an output can set, for the project a user names.
const fieldsFor = ref<Record<string, TrackerField[]>>({})
const fieldProject = ref(props.project?.externalKey ?? '')
async function loadFields(key: string, c: { scope: TrackerConnectionScope, id: string }) {
  try {
    fieldsFor.value[key] = (await api()?.listFields(c, fieldProject.value.trim())) ?? []
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  }
}

async function save() {
  try {
    await api()?.updateBoard(props.board.id, name.value.trim(), stages.value)
    emit('changed')
  } catch (e) {
    error.value = errorText(e)
  }
}

async function remove() {
  if (!confirming.value) {
    confirming.value = true
    return
  }
  try {
    await api()?.deleteBoard(props.board.id)
    emit('changed')
  } catch (e) {
    error.value = errorText(e)
  }
}

const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <form
    class="flex max-h-[60svh] flex-col gap-2.5 overflow-y-auto border border-line p-3.5"
    @submit.prevent="save"
  >
    <p
      v-if="error"
      role="alert"
      class="m-0 text-[12.5px] text-bad"
    >
      {{ error }}
    </p>
    <div class="flex flex-wrap items-center gap-2 text-[12.5px] text-toned">
      <input
        v-model="name"
        :aria-label="tt('Name of the board')"
        :class="[inputClass, 'w-[240px]']"
      >
      <span class="text-muted">{{ board.scope === BoardScope.PERSONAL ? tt('Yours alone: your connections or company ones.') : tt('Shared with the project: company connections only.') }}</span>
    </div>
    <div
      v-for="(s, i) in stages"
      :key="s.id || i"
      class="flex flex-col gap-1.5 border border-line p-2.5"
    >
      <div class="flex items-center gap-2">
        <input
          v-model="s.name"
          :placeholder="tt('Name of the stage')"
          :aria-label="tt('Name of the stage')"
          :class="[inputClass, 'min-w-0 flex-1']"
        >
        <HBtn
          size="sm"
          variant="ghost"
          :disabled="i === 0"
          :aria-label="tt('Move {0} earlier', [s.name])"
          @click="moveStage(i, -1)"
        >
          ←
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          :disabled="i === stages.length - 1"
          :aria-label="tt('Move {0} later', [s.name])"
          @click="moveStage(i, 1)"
        >
          →
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          :disabled="stages.length === 1"
          :aria-label="tt('Remove {0}', [s.name])"
          @click="stages.splice(i, 1)"
        >
          {{ tt('Remove') }}
        </HBtn>
      </div>

      <div
        v-for="(inp, n) in s.inputs"
        :key="inp.id || `i${n}`"
        class="flex flex-col gap-1 border-l-2 border-line pl-2"
      >
        <div class="flex flex-wrap items-center gap-2 text-[12px] text-toned">
          <span class="w-[46px]">{{ tt('Input') }}</span>
          <HSelect
            v-if="known(inp.connection)"
            :model-value="keyOf(inp.connection)"
            class="w-[170px]"
            :options="options"
            :aria-label="tt('Connection')"
            @update:model-value="inp.connection = parseRef(String($event))"
          />
          <span
            v-else
            class="text-muted"
            :title="inp.connection.id"
          >{{ tt('A connection on another device') }}</span>
          <input
            v-model="inp.query"
            :placeholder="queryExample(inp.connection)"
            :aria-label="tt('Query')"
            :class="[inputClass, 'min-w-[180px] flex-1 font-mono']"
          >
          <label class="flex items-center gap-1">
            {{ tt('every') }}
            <input
              v-model.number="inp.intervalSeconds"
              type="number"
              min="60"
              max="86400"
              :aria-label="tt('Seconds between polls')"
              :class="[inputClass, 'w-[70px]']"
            >
            {{ tt('s') }}
          </label>
          <HBtn
            size="sm"
            :disabled="!inp.query.trim() || !known(inp.connection)"
            @click="testQuery(`i${i}:${n}`, inp.connection, inp.query)"
          >
            {{ tt('Test') }}
          </HBtn>
          <HBtn
            size="sm"
            variant="ghost"
            @click="s.inputs!.splice(n, 1)"
          >
            {{ tt('Remove') }}
          </HBtn>
        </div>
        <p
          v-if="results[`i${i}:${n}`]?.error"
          class="m-0 text-[12px] text-bad"
        >
          {{ results[`i${i}:${n}`]!.error }}
        </p>
        <p
          v-else-if="results[`i${i}:${n}`]"
          class="m-0 text-[12px] text-muted"
        >
          {{ results[`i${i}:${n}`]!.issues!.length ? results[`i${i}:${n}`]!.issues!.map(x => x.id).join(', ') : tt('Nothing matches.') }}
        </p>
      </div>

      <div
        v-for="(out, n) in s.outputs"
        :key="out.id || `o${n}`"
        class="flex flex-col gap-1 border-l-2 border-line pl-2"
      >
        <div class="flex flex-wrap items-center gap-2 text-[12px] text-toned">
          <span class="w-[46px]">{{ tt('Output') }}</span>
          <HSelect
            v-if="known(out.connection)"
            :model-value="keyOf(out.connection)"
            class="w-[170px]"
            :options="options"
            :aria-label="tt('Connection')"
            @update:model-value="out.connection = parseRef(String($event))"
          />
          <span
            v-else
            class="text-muted"
          >{{ connectionName(out.connection) }}</span>
          <HBtn
            size="sm"
            variant="ghost"
            @click="s.outputs!.splice(n, 1)"
          >
            {{ tt('Remove') }}
          </HBtn>
        </div>
        <div
          v-for="(f, k) in out.fields"
          :key="k"
          class="flex flex-wrap items-center gap-2 text-[12px] text-toned"
        >
          <span class="w-[46px]">{{ tt('sets') }}</span>
          <input
            v-model="f.name"
            :placeholder="tt('Field, such as Stage')"
            :aria-label="tt('Field')"
            :list="`fields-${i}-${n}`"
            :class="[inputClass, 'w-[150px]']"
          >
          <datalist :id="`fields-${i}-${n}`">
            <option
              v-for="x in fieldsFor[`o${i}:${n}`] ?? []"
              :key="x.name"
              :value="x.name"
            />
          </datalist>
          <input
            v-model="f.value"
            :placeholder="tt('Value, such as Review')"
            :aria-label="tt('Value')"
            :class="[inputClass, 'w-[150px]']"
          >
          <input
            v-model="fieldProject"
            :placeholder="tt('Project to read fields from')"
            :aria-label="tt('Project to read fields from')"
            :class="[inputClass, 'w-[170px]']"
          >
          <HBtn
            size="sm"
            variant="ghost"
            :disabled="!fieldProject.trim() || !known(out.connection)"
            @click="loadFields(`o${i}:${n}`, out.connection)"
          >
            {{ tt('Load fields') }}
          </HBtn>
        </div>
        <input
          v-model="out.comment"
          :placeholder="tt('A comment to post as well (optional)')"
          :aria-label="tt('Comment')"
          :class="inputClass"
        >
      </div>

      <div
        v-if="connections.length"
        class="flex gap-2"
      >
        <HBtn
          size="sm"
          variant="ghost"
          @click="addInput(s)"
        >
          {{ tt('Add an input') }}
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          @click="addOutput(s)"
        >
          {{ tt('Add an output') }}
        </HBtn>
      </div>
    </div>
    <p
      v-if="!connections.length"
      class="m-0 text-[12px] text-muted"
    >
      {{ tt('No connection to use yet: add one in Settings, Trackers. Stages work without one.') }}
    </p>
    <div class="flex items-center gap-2">
      <HBtn
        size="sm"
        @click="addStage"
      >
        {{ tt('Add a stage') }}
      </HBtn>
      <span class="flex-1" />
      <HBtn
        size="md"
        variant="primary"
        type="submit"
      >
        {{ tt('Save') }}
      </HBtn>
      <HBtn
        size="md"
        variant="ghost"
        @click="emit('close')"
      >
        {{ tt('Close') }}
      </HBtn>
      <HBtn
        size="md"
        tone="danger"
        @click="remove"
      >
        {{ confirming ? tt('Delete with all its issues') : tt('Delete') }}
      </HBtn>
    </div>
  </form>
</template>
