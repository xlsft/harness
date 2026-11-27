<script setup lang="ts">
// Settings > Hosts: always-on hosts (docs/design/hosts.md). Everyone sees and registers their own; roles that read hosts see
// all of them with load and workers, and admins register team hosts and set their limits. The server decides all of it.
import { formatBytes, hostLoadText, isolationText, workStateText, type HostView, type HostWorkView } from '~/lib/hosts/types'
import type { GroupView } from '~/lib/enterprise/types'

const store = useSessionStore()
const access = useAccess()
const toast = useToast()

const all = ref<HostView[]>([])
// Runners of cloud sessions have their own section (SettingsRunners).
const hosts = computed(() => all.value.filter(h => h.scope !== 'cloud'))
const runners = computed(() => all.value.filter(h => h.scope === 'cloud'))
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
    all.value = h
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
  toast.add({ title: tt('Copied') })
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
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ tt('Always-on machines') }}
    </h2>
    <p class="set-sub">
      {{ tt('A server or container of yours that stays online, so work goes on while your laptop sleeps: it takes new sessions, scheduled runs whose computer is away, and auto-fix jobs. To add one, run') }} <code>harness-client daemon</code> {{ tt('on a server or in a container (') }}<code>deploy/compose.host.yml</code>{{ tt('), with the token you get here.') }}
    </p>

    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="token"
      class="mb-3 flex flex-col gap-2 border border-accent p-3 text-[12.5px]"
    >
      <span class="text-strong">{{ tt('The machine\'s token. It is not shown again: put it in HARNESS_DEVICE_TOKEN, or a secret file for HARNESS_DEVICE_TOKEN_FILE.') }}</span>
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

    <section
      :aria-label="tt('Machines')"
      class="mb-8 flex flex-col"
    >
      <div class="overflow-hidden border border-default text-[12.5px]">
        <div
          v-for="h in hosts"
          :key="h.id"
          class="flex flex-col border-b border-default"
        >
          <div class="flex items-center gap-3 px-3 py-2.5">
            <SettingsMonogram
              icon="box"
              :dim="!h.online"
            />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="flex flex-wrap items-center gap-2 text-[12.5px] text-strong">
                {{ h.name }}
                <span class="border border-default px-1 text-[10px] leading-[14px] text-muted">{{ h.scope === 'team' ? tt('Team: {0}', [h.groupName || 'no group']) : tt('Personal') }}</span>
                <span
                  class="text-[11px]"
                  :class="h.online ? 'text-ok' : 'text-dimmed'"
                >{{ hostLoadText(h) }}</span>
                <span
                  v-if="h.draining"
                  class="text-[11px] text-warn"
                >{{ tt('draining') }}</span>
              </span>
              <span class="truncate text-[11.5px] text-muted">
                {{ isolationText(h) }} · {{ tt('Sandbox: {0}', [h.sandboxDriver || 'none on this host']) }}<template v-if="h.os"> · {{ h.os }}/{{ h.arch }}<template v-if="h.cpuCount">{{ tt(', {0} CPUs, load {1}', [h.cpuCount, h.loadAverage.toFixed(1)]) }}</template></template><template v-if="h.diskFreeBytes"> · {{ tt('{0} free', [formatBytes(h.diskFreeBytes)]) }}</template><template v-if="h.clientVersion"> · {{ h.clientVersion }}</template>
              </span>
            </span>
            <span
              v-if="h.ownerName && canAll"
              class="text-[11px] text-muted max-sm:hidden"
            >{{ h.ownerName }}</span>
            <HBtn
              size="sm"
              tone="danger"
              :aria-label="tt('Remove {0}', [h.name])"
              @click="remove(h)"
            >
              {{ confirming === h.id ? tt('Remove: sure?') : tt('Remove') }}
            </HBtn>
          </div>
          <div class="flex flex-col gap-2 pr-3 pb-3 pl-[52px] text-[12px]">
            <p
              v-if="h.unavailableReason"
              class="m-0 text-warn"
            >
              {{ h.unavailableReason }}
            </p>
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
                {{ tt('Sessions at once') }}
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
                {{ tt('Per user') }}
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
                  :label="tt('Takes my work while my machines are away')"
                  @update:model-value="setAway(h, $event)"
                />
                {{ tt('Takes my work while my machines are away') }}
              </span>
              <span class="flex items-center gap-1.5 text-muted">
                <HToggle
                  :model-value="h.draining"
                  :label="tt('Takes no new work')"
                  @update:model-value="setDraining(h, $event)"
                />
                {{ tt('Takes no new work') }}
              </span>
            </div>
            <label
              v-if="canWrite"
              class="flex flex-col gap-1 text-muted"
            >
              {{ tt('Repositories it may use the connection\'s own identity on, one per line, like github.com/acme/*. Elsewhere each user needs their own account connected.') }}
              <textarea
                :value="h.allowedRepositories.join('\n')"
                rows="2"
                class="border border-line bg-transparent p-2 text-[12.5px] text-strong outline-none focus:border-accent"
                @change="setRepositories(h, ($event.target as HTMLTextAreaElement).value)"
              />
            </label>
          </div>
        </div>

        <div class="flex flex-col">
          <div class="flex items-center gap-3 px-3 py-2.5">
            <SettingsMonogram
              icon="plus"
              :dim="!hosts.length"
            />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="text-[12.5px] text-strong">{{ tt('Add a machine') }}</span>
              <span class="truncate text-[11.5px] text-muted">{{ tt('A server or container that runs harness-client daemon with the token you get here.') }}</span>
            </span>
            <HBtn
              v-if="!adding"
              size="sm"
              @click="adding = true"
            >
              <HIcon
                name="plus"
                :size="12"
              />{{ tt('Add') }}
            </HBtn>
          </div>
          <div
            v-if="adding"
            class="flex flex-col gap-2 border-t border-default py-3 pr-3 pl-[52px] text-[12px]"
          >
            <label class="flex flex-col gap-1 text-muted">
              {{ tt('Name') }}
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
                  :label="tt('For a team')"
                />
                {{ tt('For a team') }}
              </span>
              <template v-if="draft.team">
                <p class="m-0 text-warn">
                  {{ tt('A machine shared by a team must run as root on Linux with') }} <code>--shared</code>{{ tt(', so each member\'s work runs under a Unix user of its own, or it takes no work. Members still share the kernel, the loopback network and CPU; see docs/design/hosts.md. Give people who must not trust each other a machine each.') }}
                </p>
                <label class="flex flex-col gap-1 text-muted">
                  {{ tt('Group') }}
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
                  {{ tt('Sessions at once per user (0: no limit)') }}
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
              {{ tt('Sessions at once') }}
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
                {{ tt('Add machine') }}
              </HBtn>
              <HBtn
                size="sm"
                @click="adding = false"
              >
                {{ tt('Close') }}
              </HBtn>
            </div>
          </div>
        </div>
      </div>
    </section>

    <SettingsRunners
      v-if="runners.length || canWrite"
      :runners="runners"
      :groups="groups"
      @changed="load"
    />

    <section
      v-if="work.length"
      :aria-label="tt('Your work on always-on machines')"
      class="mb-8 flex flex-col"
    >
      <h3 class="m-0 mb-3 text-[13px] font-semibold text-strong">
        {{ tt('Your work on always-on machines') }}
      </h3>
      <div class="overflow-hidden border border-default text-[12.5px]">
        <div
          v-for="(w, i) in work.slice(0, 20)"
          :key="w.id"
          class="flex flex-wrap items-center gap-2 px-3 py-2"
          :class="{ 'border-t border-default': i > 0 }"
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
          >{{ tt('Pull request') }}</a>
          <HBtn
            v-if="liveStates.includes(w.state)"
            size="sm"
            @click="cancel(w)"
          >
            {{ tt('Cancel') }}
          </HBtn>
        </div>
      </div>
    </section>
  </div>
</template>
