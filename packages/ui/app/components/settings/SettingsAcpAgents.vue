<script setup lang="ts">
// Settings > Models, "Agents": external agent CLIs from the ACP registry (Claude Code, Codex, Gemini CLI, goose,
// ...) that run a whole session on your own subscription. Installed agents first, with their sign-in, version and
// the subscription limits they report; the rest of the registry below, filtered. Installs, sign-ins and limits
// stay on this computer: nothing is synced, nobody else sees them.
import { AcpAuthMethod_Kind, AcpAuthState, AcpModelsStatus, type AcpAgent, type AcpAgentList, type AcpApi, type AcpAuthMethod } from '~/lib/acp/types'

const store = useSessionStore()
const toast = useToast()
const list = ref<AcpAgentList>({ agents: [], registryFetchedAt: '', registryError: '', runtimes: [], sessionSwitchRefusal: '' })
const loading = ref(true)
const error = ref('')
const filter = ref('')
const browsing = ref(false)

function api(): AcpApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.acp()
}
function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}
async function load(refresh = false) {
  try {
    const next = await store.refreshAcpAgents({ refresh })
    if (next) list.value = next
    error.value = ''
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}
onMounted(() => load())
// Model probes finish on their own: the store lists the agents again while one runs.
watch(() => store.acpAgents, (agents) => {
  if (!loading.value) list.value.agents = agents
})

const installed = computed(() => list.value.agents.filter(a => a.installed))
const others = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return list.value.agents.filter(a => !a.installed && (!q || `${a.name} ${a.id} ${a.description}`.toLowerCase().includes(q)))
})

function replace(a: AcpAgent) {
  list.value.agents = list.value.agents.map(x => (x.id === a.id ? a : x))
  store.putAcpAgent(a)
}

// Install / update with progress.
const progress = reactive<Record<string, { pct: number, text: string }>>({})
async function install(a: AcpAgent, update = false) {
  progress[a.id] = { pct: 0, text: 'Starting…' }
  try {
    const call = update ? api().updateAgent : api().installAgent
    const next = await call.call(api(), a.id, (p) => {
      progress[a.id] = { pct: p.bytesTotal ? Number(p.bytesDone) / Number(p.bytesTotal) : progress[a.id]?.pct ?? 0, text: p.message }
    })
    replace(next)
  } catch (e) {
    error.value = message(e)
  } finally {
    Reflect.deleteProperty(progress, a.id)
  }
}
async function uninstall(a: AcpAgent) {
  try {
    replace(await api().uninstallAgent(a.id))
  } catch (e) {
    error.value = message(e)
  }
}

// Sign-in: an agent method signs in by itself; a terminal method runs the agent's login command (shown to copy,
// or opened in a terminal), then "Check again".
const pendingLogin = ref<{ agentId: string, line: string, env: string[] } | null>(null)
async function signIn(a: AcpAgent, m: AcpAuthMethod) {
  try {
    const r = await api().authenticate({ agentId: a.id, methodId: m.id })
    if (r.agent) replace(r.agent)
    if (r.authenticated) {
      toast.add({ title: tt('Signed in to {0}', [a.name]), color: 'neutral' })
      pendingLogin.value = null
    } else if (r.terminal || r.envVars.length) {
      pendingLogin.value = { agentId: a.id, line: r.terminal?.shellLine ?? '', env: r.envVars }
    }
  } catch (e) {
    error.value = message(e)
  }
}
async function verify(a: AcpAgent) {
  try {
    const r = await api().authenticate({ agentId: a.id, verify: true })
    if (r.agent) replace(r.agent)
    if (r.authenticated) pendingLogin.value = null
    else toast.add({ title: tt('Not signed in yet'), description: tt('Finish the login, then check again.'), color: 'neutral' })
  } catch (e) {
    error.value = message(e)
  }
}
async function copy(text: string) {
  await navigator.clipboard.writeText(text)
  toast.add({ title: tt('Command copied'), color: 'neutral' })
}

const icon = (a: AcpAgent) => a.iconSvg ? `url("data:image/svg+xml;utf8,${encodeURIComponent(a.iconSvg)}")` : ''
function stateText(a: AcpAgent) {
  if (!a.available) return a.unavailableReason || 'unavailable'
  if (a.authState === AcpAuthState.REQUIRED) return 'sign-in needed'
  if (a.authState === AcpAuthState.AUTHENTICATED) return 'signed in'
  return 'ready'
}
// The agent's models as the client knows them, for the installed agent's row.
function modelsText(a: AcpAgent) {
  const names = a.models.map(m => m.name).join(', ')
  switch (a.modelsStatus) {
    case AcpModelsStatus.PROBING: return names ? `${names} · checking…` : 'Finding models…'
    case AcpModelsStatus.FAILED: return `${names ? `${names} · ` : ''}could not read its models: ${a.modelsError}`
    case AcpModelsStatus.OK: return names
    default: return ''
  }
}
function resets(at: string) {
  const ms = Date.parse(at) - Date.now()
  if (!at || Number.isNaN(ms) || ms <= 0) return ''
  const h = Math.floor(ms / 3_600_000)
  return h >= 24 ? `resets in ${Math.round(h / 24)}d` : h ? `resets in ${h}h` : `resets in ${Math.max(1, Math.round(ms / 60_000))}m`
}
</script>

<template>
  <section
    :aria-label="tt('Agents')"
    class="mb-8 flex flex-col"
  >
    <div class="mb-1 flex items-center gap-2">
      <h3 class="m-0 flex-1 text-[13px] font-semibold text-strong">
        {{ tt('Agents') }}
      </h3>
      <HBtn
        size="sm"
        @click="load(true)"
      >
        {{ tt('Refresh') }}
      </HBtn>
    </div>
    <p class="m-0 mb-3 text-[12px] text-muted">
      {{ tt('Agent CLIs that run a whole session on your own subscription, through the Agent Client Protocol. Installed and signed in on this computer only; their limits are visible to you alone.') }}
    </p>

    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 flex items-start gap-2 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      <span class="flex-1">{{ error }}</span>
      <HIconBtn
        icon="x"
        :label="tt('Dismiss')"
        :size="26"
        :icon-size="12"
        @click="error = ''"
      />
    </p>
    <p
      v-if="loading"
      class="m-0 py-2 text-[12.5px] text-muted"
    >
      {{ tt('Loading the registry…') }}
    </p>

    <div
      v-else
      class="overflow-hidden border border-default"
    >
      <div
        v-for="(a, i) in installed"
        :key="a.id"
        class="flex flex-col"
        :class="i && 'border-t border-default'"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <span
            v-if="a.iconSvg"
            aria-hidden="true"
            class="size-7 shrink-0 text-strong [mask-position:center] [mask-repeat:no-repeat] [mask-size:16px]"
            :style="{ maskImage: icon(a), backgroundColor: 'currentColor' }"
          />
          <SettingsMonogram
            v-else
            :text="a.name.slice(0, 2).toUpperCase()"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ a.name }}
              <span
                class="text-[11px]"
                :class="a.authState === AcpAuthState.REQUIRED || !a.available ? 'text-warn' : 'text-ok'"
              >{{ stateText(a) }}</span>
              <span class="text-[11px] text-muted">{{ a.installedVersion }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ a.description }}</span>
            <span
              v-if="modelsText(a)"
              class="truncate text-[11.5px]"
              :class="a.modelsStatus === AcpModelsStatus.FAILED ? 'text-warn' : 'text-muted'"
              :title="modelsText(a)"
            >{{ modelsText(a) }}</span>
          </span>
          <HBtn
            v-if="a.updateAvailable && !progress[a.id]"
            size="sm"
            @click="install(a, true)"
          >
            {{ tt('Update to {0}', [a.latestVersion]) }}
          </HBtn>
          <HBtn
            size="sm"
            tone="danger"
            :disabled="!!progress[a.id]"
            @click="uninstall(a)"
          >
            {{ tt('Uninstall') }}
          </HBtn>
        </div>

        <div
          v-if="progress[a.id]"
          class="flex items-center gap-2 py-2 pr-3 pl-[52px] text-[11.5px] text-muted"
        >
          <span class="min-w-0 flex-1 truncate">{{ progress[a.id]!.text }}</span>
          <span class="h-1 w-[110px] bg-raised"><span
            class="block h-full bg-accent"
            :style="{ width: `${Math.round(progress[a.id]!.pct * 100)}%` }"
          /></span>
        </div>

        <!-- Sign-in -->
        <div
          v-if="a.authState === AcpAuthState.REQUIRED && a.authMethods.length"
          class="flex flex-col gap-2 border-t border-default py-2.5 pr-3 pl-[52px] text-[12px]"
        >
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-muted">{{ tt('Sign in with') }}</span>
            <HBtn
              v-for="m in a.authMethods"
              :key="m.id"
              size="sm"
              :title="m.description"
              @click="signIn(a, m)"
            >
              {{ m.name }}<template v-if="m.kind === AcpAuthMethod_Kind.TERMINAL">
                {{ tt('(terminal)') }}
              </template>
            </HBtn>
          </div>
          <div
            v-if="pendingLogin?.agentId === a.id"
            class="flex flex-col gap-1.5"
          >
            <template v-if="pendingLogin.line">
              <span class="text-muted">{{ tt('Run this in a terminal and finish the login there:') }}</span>
              <code class="flex items-center gap-2 border border-default bg-surface px-2.5 py-1.5 font-mono text-[11.5px] text-strong">
                <span class="min-w-0 flex-1 truncate">{{ pendingLogin.line }}</span>
                <HIconBtn
                  icon="copy"
                  label="Copy command"
                  :size="26"
                  :icon-size="12"
                  @click="copy(pendingLogin.line)"
                />
              </code>
            </template>
            <span
              v-if="pendingLogin.env.length"
              class="text-muted"
            >{{ tt('Set {0} in the folder\'s environment (Edit environment).', [pendingLogin.env.join(', ')]) }}</span>
            <div>
              <HBtn
                size="sm"
                variant="primary"
                @click="verify(a)"
              >
                {{ tt('Check again') }}
              </HBtn>
            </div>
          </div>
        </div>

        <!-- Subscription limits the agent reports -->
        <div
          v-if="a.limits?.windows.length"
          class="flex flex-col gap-1.5 border-t border-default py-2.5 pr-3 pl-[52px] text-[11.5px]"
        >
          <span
            v-if="a.limits.planName"
            class="text-muted"
          >{{ tt('Plan {0}', [a.limits.planName]) }}</span>
          <div
            v-for="w in a.limits.windows"
            :key="w.label"
            class="flex items-center gap-2"
          >
            <span class="w-14 shrink-0 text-muted">{{ w.label }}</span>
            <span class="h-1 flex-1 bg-raised"><span
              class="block h-full"
              :class="w.usedPercent >= 90 ? 'bg-bad' : w.usedPercent >= 70 ? 'bg-warn' : 'bg-accent'"
              :style="{ width: `${Math.min(100, w.usedPercent)}%` }"
            /></span>
            <span class="w-9 shrink-0 text-right text-strong tabular-nums">{{ Math.round(w.usedPercent) }}%</span>
            <span class="w-24 shrink-0 text-muted">{{ resets(w.resetsAt) }}</span>
          </div>
        </div>
      </div>

      <!-- The registry -->
      <div
        class="flex flex-col"
        :class="installed.length && 'border-t border-default'"
      >
        <button
          type="button"
          class="flex items-center gap-3 px-3 py-2.5 text-left hover:bg-hover"
          :aria-expanded="browsing"
          @click="browsing = !browsing"
        >
          <SettingsMonogram
            icon="plus"
            dim
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="text-[12.5px] text-strong">{{ tt('Add an agent') }}</span>
            <span class="truncate text-[11.5px] text-muted">{{ tt('{0} in the ACP registry', [others.length]) }}<template v-if="list.registryError">
              {{ tt('· offline, from the cache') }}
            </template></span>
          </span>
          <HIcon
            name="right"
            :size="12"
            class="shrink-0 text-muted transition-transform"
            :class="browsing && 'rotate-90'"
          />
        </button>
        <template v-if="browsing">
          <div class="border-t border-default px-3 py-2">
            <input
              v-model="filter"
              :placeholder="tt('Filter agents')"
              :aria-label="tt('Filter agents')"
              class="h-8 w-full border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent"
            >
          </div>
          <ul class="m-0 flex max-h-[320px] list-none flex-col overflow-y-auto p-0">
            <li
              v-for="a in others"
              :key="a.id"
              class="flex items-center gap-3 border-t border-default px-3 py-2 text-[12px]"
            >
              <span
                v-if="a.iconSvg"
                aria-hidden="true"
                class="size-7 shrink-0 text-muted [mask-position:center] [mask-repeat:no-repeat] [mask-size:16px]"
                :style="{ maskImage: icon(a), backgroundColor: 'currentColor' }"
              />
              <SettingsMonogram
                v-else
                :text="a.name.slice(0, 2).toUpperCase()"
                dim
              />
              <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                <span class="flex items-center gap-2 text-strong">{{ a.name }}<span class="text-[11px] text-muted">{{ a.latestVersion }}</span></span>
                <span class="truncate text-[11.5px] text-muted">{{ a.available ? a.description : a.unavailableReason }}</span>
              </span>
              <span
                v-if="progress[a.id]"
                class="h-1 w-[90px] shrink-0 bg-raised"
              ><span
                class="block h-full bg-accent"
                :style="{ width: `${Math.round(progress[a.id]!.pct * 100)}%` }"
              /></span>
              <HBtn
                v-else
                size="sm"
                :disabled="!a.available"
                @click="install(a)"
              >
                {{ tt('Install') }}
              </HBtn>
            </li>
          </ul>
        </template>
      </div>
    </div>
  </section>
</template>
