<script setup lang="ts">
// Settings > Hosts > Cloud runners (docs/design/cloud-sessions.md): runner nodes that start a container per session. Admins
// add them and set their image, runtime, limits and timeouts; everyone sees the runners there for them and their own cloud
// sessions. Every rule is the server's; this screen only edits and shows.
import { cloudStateText, formatBytes, runnerConfigFromForm, runnerForm, type CloudSessionView, type HostView, type RunnerConfigForm } from '~/lib/hosts/types'
import type { GroupView } from '~/lib/enterprise/types'

const props = defineProps<{ runners: HostView[], groups: GroupView[] }>()
const emit = defineEmits<{ changed: [] }>()
const store = useSessionStore()
const access = useAccess()
const toast = useToast()

const canWrite = computed(() => access.can('hosts.write'))
const canAll = computed(() => access.can('hosts.read'))
const error = ref('')
const token = ref('')
const adding = ref(false)
const confirming = ref('')
const sessions = ref<Record<string, CloudSessionView[]>>({})
const mine = ref<CloudSessionView[]>([])
const draft = reactive({ name: '', groupId: '', maxSessions: '8' })

const forms = reactive<Record<string, RunnerConfigForm>>({})

const newConfig = reactive<RunnerConfigForm>(runnerForm(null))

watch(() => props.runners, (rs) => {
  for (const r of rs) if (!forms[r.id]) forms[r.id] = runnerForm(r.cloud)
}, { immediate: true })

function api() {
  return store.transport!.hosts()
}

async function run(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
    return true
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
    return false
  }
}

async function loadSessions() {
  await run(async () => {
    mine.value = await api().listCloudSessions()
    if (canAll.value) {
      const all: Record<string, CloudSessionView[]> = {}
      for (const r of props.runners) all[r.id] = await api().listCloudSessions(r.id)
      sessions.value = all
    }
  })
}
watch(() => props.runners.map(r => r.id).join(), loadSessions, { immediate: true })

async function create() {
  await run(async () => {
    const res = await api().create({ name: draft.name, team: false, groupId: draft.groupId, maxSessions: Number(draft.maxSessions) || 0, maxSessionsPerUser: 0, allowedRepositories: [], runner: runnerConfigFromForm(newConfig) })
    token.value = res.deviceToken
    draft.name = ''
    adding.value = false
    emit('changed')
  })
}

async function save(r: HostView) {
  if (await run(() => api().update(r.id, { runner: runnerConfigFromForm(forms[r.id]!) }))) {
    toast.add({ title: tt('Saved') })
    emit('changed')
  }
}

async function update(r: HostView, changes: Parameters<ReturnType<typeof api>['update']>[1]) {
  if (await run(() => api().update(r.id, changes))) emit('changed')
}

async function remove(r: HostView) {
  if (confirming.value !== r.id) {
    confirming.value = r.id
    return
  }
  confirming.value = ''
  if (await run(() => api().remove(r.id))) emit('changed')
}

async function stop(c: CloudSessionView, end: boolean) {
  if (await run(() => api().stopCloudSession(c.id, end))) await loadSessions()
}

async function copyToken() {
  await navigator.clipboard.writeText(token.value)
  toast.add({ title: tt('Copied') })
}

const live = (c: CloudSessionView) => c.state === 'starting' || c.state === 'running' || c.state === 'idle'
const FIELD = 'h-8 min-w-0 border border-line bg-transparent px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div class="flex flex-col gap-3">
    <div>
      <h3 class="m-0 text-[13px] font-semibold text-strong">
        {{ tt('Cloud runners') }}
      </h3>
      <p class="m-0 mt-1 text-muted">
        {{ tt('A runner starts a container for each cloud session and removes it when the session has been idle; the next message starts a new one. Run') }} <code>harness-client runner</code> {{ tt('next to Docker or Podman (') }}<code>deploy/compose.runner.yml</code>{{ tt('), with the token you get here.') }}
      </p>
    </div>

    <p
      v-if="error"
      class="m-0 border border-bad px-2.5 py-1.5 text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="token"
      class="flex flex-col gap-2 border border-accent p-3"
    >
      <span class="text-strong">{{ tt('The runner\'s token. It is not shown again: put it in a secret file for HARNESS_DEVICE_TOKEN_FILE.') }}</span>
      <code class="break-all text-[11.5px] text-toned">{{ token }}</code>
      <div class="flex gap-2">
        <HBtn
          size="sm"
          @click="copyToken"
        >
          {{ tt('Copy') }}
        </HBtn>
        <HBtn
          size="sm"
          @click="token = ''"
        >
          {{ tt('Done') }}
        </HBtn>
      </div>
    </div>

    <div
      v-for="r in runners"
      :key="r.id"
      class="flex flex-col gap-2 border border-line p-3"
    >
      <div class="flex flex-wrap items-center gap-2">
        <span class="font-semibold text-strong">{{ r.name }}</span>
        <span class="border border-default px-1 text-[10px] leading-[14px] text-muted">{{ r.groupName ? tt('For {0}', [r.groupName]) : tt('For everyone') }}</span>
        <span :class="r.online ? 'text-toned' : 'text-muted'">{{ r.online ? tt('online') : tt('offline') }}</span>
        <span
          v-if="r.draining"
          class="text-warn"
        >{{ tt('draining') }}</span>
        <span class="flex-1" />
        <span
          v-if="r.cloudStats"
          class="text-muted"
        >{{ tt('{0} starting, {1} running, {2} idle of {3}; {4} removed and {5} failed today', [r.cloudStats.starting, r.cloudStats.running, r.cloudStats.idle, r.maxSessions, r.cloudStats.removed, r.cloudStats.failed]) }}</span>
      </div>
      <p
        v-if="r.unavailableReason"
        class="m-0 text-warn"
      >
        {{ r.unavailableReason }}
      </p>
      <div
        v-if="r.clientVersion"
        class="flex flex-wrap gap-x-4 gap-y-1 text-muted"
      >
        <span>{{ r.os }}/{{ r.arch }}<template v-if="r.cpuCount">{{ tt(', {0} CPUs', [r.cpuCount]) }}</template></span>
        <span>{{ r.clientVersion }}</span>
      </div>
      <template v-if="canWrite && forms[r.id]">
        <div class="flex flex-wrap items-center gap-3">
          <label class="flex items-center gap-1.5 text-muted">
            {{ tt('Containers at once') }}
            <input
              :value="r.maxSessions"
              type="number"
              min="1"
              :class="[FIELD, 'w-16']"
              @change="update(r, { maxSessions: Number(($event.target as HTMLInputElement).value) || 0 })"
            >
          </label>
          <label class="flex items-center gap-1.5 text-muted">
            {{ tt('For') }}
            <select
              :value="r.groupId"
              :class="FIELD"
              @change="update(r, { groupId: ($event.target as HTMLSelectElement).value })"
            >
              <option value="">{{ tt('Everyone') }}</option>
              <option
                v-for="g in groups"
                :key="g.id"
                :value="g.id"
              >
                {{ g.name }}
              </option>
            </select>
          </label>
          <span class="flex items-center gap-1.5 text-muted">
            <HToggle
              :model-value="r.draining"
              :label="tt('Takes no new sessions')"
              @update:model-value="update(r, { draining: $event })"
            />
            {{ tt('Takes no new sessions') }}
          </span>
        </div>
        <SettingsRunnerConfig v-model="forms[r.id]!" />
        <div class="flex gap-2">
          <HBtn
            size="sm"
            variant="primary"
            @click="save(r)"
          >
            {{ tt('Save') }}
          </HBtn>
          <span class="flex-1" />
          <HBtn
            size="sm"
            @click="remove(r)"
          >
            {{ confirming === r.id ? tt('Remove: sure?') : tt('Remove') }}
          </HBtn>
        </div>
      </template>
      <div
        v-if="sessions[r.id]?.length"
        class="flex flex-col"
      >
        <div
          v-for="c in sessions[r.id]"
          :key="c.id"
          class="flex flex-wrap items-center gap-2 border-b border-line py-1"
        >
          <span class="min-w-0 flex-1 truncate text-toned">{{ c.repositoryKey }}</span>
          <span class="text-muted">{{ c.userName }}</span>
          <span class="text-muted">{{ cloudStateText(c.state) }}</span>
          <span
            v-if="c.diskBytes"
            class="text-muted"
          >{{ formatBytes(c.diskBytes) }}</span>
          <span
            v-if="c.error"
            class="truncate text-warn"
            :title="c.error"
          >{{ c.error }}</span>
          <HBtn
            v-if="live(c) && canWrite"
            size="sm"
            @click="stop(c, false)"
          >
            {{ tt('Remove container') }}
          </HBtn>
        </div>
      </div>
    </div>

    <p
      v-if="!runners.length && !adding"
      class="m-0 text-muted"
    >
      {{ tt('No cloud runners yet.') }}
    </p>

    <div
      v-if="adding"
      class="flex flex-col gap-2 border border-line p-3"
    >
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Name') }}
        <input
          v-model="draft.name"
          :class="FIELD"
          placeholder="runner-1"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('For') }}
        <select
          v-model="draft.groupId"
          :class="FIELD"
        >
          <option value="">{{ tt('Everyone') }}</option>
          <option
            v-for="g in groups"
            :key="g.id"
            :value="g.id"
          >
            {{ g.name }}
          </option>
        </select>
      </label>
      <label class="flex items-center gap-1.5 text-muted">
        {{ tt('Containers at once') }}
        <input
          v-model="draft.maxSessions"
          type="number"
          min="1"
          :class="[FIELD, 'w-16']"
        >
      </label>
      <SettingsRunnerConfig v-model="newConfig" />
      <div class="flex gap-2">
        <HBtn
          variant="primary"
          size="sm"
          :disabled="!draft.name.trim()"
          @click="create"
        >
          {{ tt('Add runner') }}
        </HBtn>
        <HBtn
          size="sm"
          @click="adding = false"
        >
          {{ tt('Close') }}
        </HBtn>
      </div>
    </div>
    <div v-else-if="canWrite">
      <HBtn
        size="sm"
        @click="adding = true"
      >
        {{ tt('Add a runner') }}
      </HBtn>
    </div>

    <div
      v-if="mine.length"
      class="flex flex-col gap-1"
    >
      <h3 class="m-0 text-[12.5px] font-semibold text-strong">
        {{ tt('Your cloud sessions') }}
      </h3>
      <div
        v-for="c in mine.slice(0, 20)"
        :key="c.id"
        class="flex flex-wrap items-center gap-2 border-b border-line py-1"
      >
        <span class="min-w-0 flex-1 truncate text-toned">{{ c.repositoryKey }}</span>
        <span class="text-muted">{{ c.hostName }}</span>
        <span class="text-muted">{{ cloudStateText(c.state) }}</span>
        <HBtn
          v-if="live(c)"
          size="sm"
          @click="stop(c, false)"
        >
          {{ tt('Remove container') }}
        </HBtn>
        <HBtn
          v-if="c.state !== 'ended'"
          size="sm"
          @click="stop(c, true)"
        >
          {{ tt('End') }}
        </HBtn>
      </div>
    </div>
  </div>
</template>
