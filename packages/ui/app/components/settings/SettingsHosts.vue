<script setup lang="ts">
// Settings > Hosts: always-on hosts (docs/design/hosts.md). Everyone sees and registers their own; roles that read hosts see
// all of them with load and workers, and admins register team hosts and set their limits. The server decides all of it.
import { formatBytes, hostLoadText, isolationText, workStateText, type HostView, type HostWorkView } from '~/lib/hosts/types'
import type { GroupView } from '~/lib/enterprise/types'

const store = useSessionStore()
const access = useAccess()
const toast = useToast()

const hosts = ref<HostView[]>([])
const work = ref<HostWorkView[]>([])
const groups = ref<GroupView[]>([])
const error = ref('')
const adding = ref(false)
const token = ref('')
const confirming = ref('')
const draft = reactive({ name: '', team: false, groupId: '', maxSessions: '4', maxSessionsPerUser: '0', allowedRepositories: '' })

const canAll = computed(() => access.can('hosts.read'))
const canWrite = computed(() => access.can('hosts.write'))

function api() {
  return store.transport!.hosts()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function run(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
    return true
  } catch (e) {
    error.value = message(e)
    return false
  }
}

async function load() {
  await run(async () => {
    await access.load()
    const [h, w] = await Promise.all([api().list(canAll.value), api().listWork()])
    hosts.value = h
    work.value = w
    if (canWrite.value && !groups.value.length) groups.value = await store.transport!.enterprise().listGroups()
  })
}

let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  load()
  timer = setInterval(load, 15_000)
})
onBeforeUnmount(() => clearInterval(timer))

const lines = (s: string) => s.split('\n').map(x => x.trim()).filter(Boolean)

async function create() {
  await run(async () => {
    const res = await api().create({ name: draft.name, team: draft.team, groupId: draft.groupId, maxSessions: Number(draft.maxSessions) || 0, maxSessionsPerUser: Number(draft.maxSessionsPerUser) || 0, allowedRepositories: lines(draft.allowedRepositories) })
    token.value = res.deviceToken
    draft.name = ''
    draft.allowedRepositories = ''
    await load()
  })
}

async function copyToken() {
  await navigator.clipboard.writeText(token.value)
  toast.add({ title: 'Copied' })
}

async function setLimit(h: HostView, key: 'maxSessions' | 'maxSessionsPerUser', value: string) {
  if (await run(() => api().update(h.id, { [key]: Number(value) || 0 }))) await load()
}

async function setRepositories(h: HostView, value: string) {
  if (await run(() => api().update(h.id, { allowedRepositories: lines(value) }))) await load()
}

async function setDraining(h: HostView, draining: boolean) {
  if (await run(() => api().update(h.id, { draining }))) await load()
}

async function setAway(h: HostView, on: boolean) {
  if (await run(() => api().setAway(on ? h.id : ''))) await load()
}

async function remove(h: HostView) {
  if (confirming.value !== h.id) {
    confirming.value = h.id
    return
  }
  confirming.value = ''
  if (await run(() => api().remove(h.id))) await load()
}

async function cancel(w: HostWorkView) {
  if (await run(() => api().cancel(w.id))) await load()
}

const liveStates: HostWorkView['state'][] = ['queued', 'preparing', 'running']
const FIELD = 'h-8 min-w-0 border border-line bg-transparent px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div class="flex flex-col gap-4 text-[12.5px]">
    <div>
      <h2 class="m-0 text-[14px] font-semibold text-strong">
        Always-on hosts
      </h2>
      <p class="m-0 mt-1 text-muted">
        A host keeps working when your laptop sleeps: it takes new sessions, schedules whose machine is away, and auto-fix jobs. Run <code>harness-client daemon</code> on a server or in a container (<code>deploy/compose.host.yml</code>), with the token you get here.
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
      <span class="text-strong">The host's token. It is not shown again: put it in HARNESS_DEVICE_TOKEN, or a secret file for HARNESS_DEVICE_TOKEN_FILE.</span>
      <code class="break-all text-[11.5px] text-toned">{{ token }}</code>
      <div class="flex gap-2">
        <HBtn
          size="sm"
          @click="copyToken"
        >
          Copy
        </HBtn>
        <HBtn
          size="sm"
          @click="token = ''"
        >
          Done
        </HBtn>
      </div>
    </div>

    <div
      v-for="h in hosts"
      :key="h.id"
      class="flex flex-col gap-2 border border-line p-3"
    >
      <div class="flex flex-wrap items-center gap-2">
        <span class="font-semibold text-strong">{{ h.name }}</span>
        <span class="border border-default px-1 text-[10px] leading-[14px] text-muted">{{ h.scope === 'team' ? `Team: ${h.groupName || 'no group'}` : 'Personal' }}</span>
        <span :class="h.online ? 'text-good' : 'text-muted'">{{ hostLoadText(h) }}</span>
        <span
          v-if="h.draining"
          class="text-warn"
        >draining</span>
        <span class="flex-1" />
        <span
          v-if="h.ownerName && canAll"
          class="text-muted"
        >{{ h.ownerName }}</span>
      </div>
      <p
        v-if="h.unavailableReason"
        class="m-0 text-warn"
      >
        {{ h.unavailableReason }}
      </p>
      <div class="flex flex-wrap gap-x-4 gap-y-1 text-muted">
        <span>{{ isolationText(h) }}</span>
        <span>Sandbox: {{ h.sandboxDriver || 'none on this host' }}</span>
        <span v-if="h.os">{{ h.os }}/{{ h.arch }}<template v-if="h.cpuCount">, {{ h.cpuCount }} CPUs, load {{ h.loadAverage.toFixed(1) }}</template></span>
        <span v-if="h.diskFreeBytes">{{ formatBytes(h.diskFreeBytes) }} free</span>
        <span v-if="h.clientVersion">{{ h.clientVersion }}</span>
      </div>
      <div
        v-if="h.workers.length"
        class="flex flex-wrap gap-x-4 gap-y-1 text-muted"
      >
        <span
          v-for="w in h.workers"
          :key="w.userId"
        >{{ w.userName }}: {{ w.online ? `${w.workingSessions} working` : 'offline' }}</span>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <label class="flex items-center gap-1.5 text-muted">
          Sessions at once
          <input
            :value="h.maxSessions"
            :disabled="h.scope === 'team' && !canWrite"
            type="number"
            min="1"
            :class="[FIELD, 'w-16']"
            @change="setLimit(h, 'maxSessions', ($event.target as HTMLInputElement).value)"
          >
        </label>
        <label
          v-if="h.scope === 'team'"
          class="flex items-center gap-1.5 text-muted"
        >
          Per user
          <input
            :value="h.maxSessionsPerUser"
            :disabled="!canWrite"
            type="number"
            min="0"
            :class="[FIELD, 'w-16']"
            @change="setLimit(h, 'maxSessionsPerUser', ($event.target as HTMLInputElement).value)"
          >
        </label>
        <span class="flex items-center gap-1.5 text-muted">
          <HToggle
            :model-value="h.awayHost"
            label="Takes my work while my machines are away"
            @update:model-value="setAway(h, $event)"
          />
          Takes my work while my machines are away
        </span>
        <span class="flex items-center gap-1.5 text-muted">
          <HToggle
            :model-value="h.draining"
            label="Takes no new work"
            @update:model-value="setDraining(h, $event)"
          />
          Takes no new work
        </span>
        <span class="flex-1" />
        <HBtn
          size="sm"
          @click="remove(h)"
        >
          {{ confirming === h.id ? 'Remove: sure?' : 'Remove' }}
        </HBtn>
      </div>
      <label
        v-if="canWrite"
        class="flex flex-col gap-1 text-muted"
      >
        Repositories it may use the connection's own identity on, one per line, like github.com/acme/*. Elsewhere each user needs their own account connected.
        <textarea
          :value="h.allowedRepositories.join('\n')"
          rows="2"
          class="border border-line bg-transparent p-2 text-[12.5px] text-strong outline-none focus:border-accent"
          @change="setRepositories(h, ($event.target as HTMLTextAreaElement).value)"
        />
      </label>
    </div>

    <p
      v-if="!hosts.length && !adding"
      class="m-0 text-muted"
    >
      No hosts yet.
    </p>

    <div
      v-if="adding"
      class="flex flex-col gap-2 border border-line p-3"
    >
      <label class="flex flex-col gap-1 text-muted">
        Name
        <input
          v-model="draft.name"
          :class="FIELD"
          placeholder="build-box"
        >
      </label>
      <template v-if="canWrite">
        <span class="flex items-center gap-1.5 text-muted">
          <HToggle
            v-model="draft.team"
            label="For a team"
          />
          For a team
        </span>
        <template v-if="draft.team">
          <p class="m-0 text-warn">
            A team host must run as root on Linux with <code>--shared</code>, so each member's work runs under a Unix user of its own, or it takes no work. Members still share the kernel, the loopback network and CPU; see docs/design/hosts.md. Give people who must not trust each other a host each.
          </p>
          <label class="flex flex-col gap-1 text-muted">
            Group
            <select
              v-model="draft.groupId"
              :class="FIELD"
            >
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
            Sessions at once per user (0: no limit)
            <input
              v-model="draft.maxSessionsPerUser"
              type="number"
              min="0"
              :class="[FIELD, 'w-16']"
            >
          </label>
        </template>
      </template>
      <label class="flex items-center gap-1.5 text-muted">
        Sessions at once
        <input
          v-model="draft.maxSessions"
          type="number"
          min="1"
          :class="[FIELD, 'w-16']"
        >
      </label>
      <div class="flex gap-2">
        <HBtn
          variant="primary"
          size="sm"
          :disabled="!draft.name.trim() || (draft.team && !draft.groupId)"
          @click="create"
        >
          Add host
        </HBtn>
        <HBtn
          size="sm"
          @click="adding = false"
        >
          Close
        </HBtn>
      </div>
    </div>
    <div v-else>
      <HBtn
        size="sm"
        @click="adding = true"
      >
        Add a host
      </HBtn>
    </div>

    <div
      v-if="work.length"
      class="flex flex-col gap-1"
    >
      <h3 class="m-0 text-[12.5px] font-semibold text-strong">
        Your work on hosts
      </h3>
      <div
        v-for="w in work.slice(0, 20)"
        :key="w.id"
        class="flex flex-wrap items-center gap-2 border-b border-line py-1"
      >
        <span class="min-w-0 flex-1 truncate text-toned">{{ w.title || w.repositoryKey }}</span>
        <span class="text-muted">{{ w.hostName }}</span>
        <span class="text-muted">{{ workStateText(w) }}</span>
        <a
          v-if="w.pullRequestUrl"
          :href="w.pullRequestUrl"
          target="_blank"
          rel="noopener"
          class="text-accent"
        >Pull request</a>
        <HBtn
          v-if="liveStates.includes(w.state)"
          size="sm"
          @click="cancel(w)"
        >
          Cancel
        </HBtn>
      </div>
    </div>
  </div>
</template>
