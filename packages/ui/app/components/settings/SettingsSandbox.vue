<script setup lang="ts">
// Settings > Sandbox: the OS sandbox of the commands the model runs. The choice is the user's and synced (UserPreferences);
// what this machine can do comes from the client (SandboxStatus). Saves at once; a failed save puts the values back.
import type { SandboxPreferences, SandboxStatus } from '~/lib/transport/types'

const store = useSessionStore()

const prefs = ref<SandboxPreferences>({ enabled: true, allowedWritePaths: [], allowedHosts: [], allowLocalhost: false, autoAllowBash: false, outsideWithoutPrompt: false })
const status = ref<SandboxStatus | null>(null)
const loaded = ref(false)
const error = ref('')
const paths = ref('')
const hosts = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function show(p: SandboxPreferences) {
  prefs.value = p
  paths.value = p.allowedWritePaths.join('\n')
  hosts.value = p.allowedHosts.join('\n')
}

onMounted(async () => {
  if (!store.transport) return
  try {
    status.value = await store.transport.getSandboxStatus()
    show((await store.transport.getUserPreferences()).sandbox)
    loaded.value = true
  } catch (e) {
    error.value = message(e)
  }
})

async function save(patch: Partial<SandboxPreferences>) {
  if (!store.transport) return
  const prev = prefs.value
  prefs.value = { ...prev, ...patch }
  try {
    show((await store.transport.updateUserPreferences({ sandbox: prefs.value })).sandbox)
    error.value = ''
  } catch (e) {
    show(prev)
    error.value = message(e)
  }
}

const lines = (s: string) => s.split('\n').map(l => l.trim()).filter(Boolean)

const unavailable = computed(() => status.value !== null && !status.value.supported)
const required = computed(() => status.value?.requiredByPolicy ?? false)
const toggle = (key: 'enabled' | 'allowLocalhost' | 'autoAllowBash' | 'outsideWithoutPrompt') => computed({
  get: () => prefs.value[key],
  set: (v: boolean) => void save({ [key]: v })
})
const enabled = computed({
  get: () => required.value || prefs.value.enabled,
  set: (v: boolean) => void save({ enabled: v })
})
const allowLocalhost = toggle('allowLocalhost')
const autoAllowBash = toggle('autoAllowBash')
const outsideWithoutPrompt = toggle('outsideWithoutPrompt')
</script>

<template>
  <section
    :aria-label="tt('Sandbox')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Sandbox') }}
    </h2>
    <p class="set-sub">
      {{ tt('The agent\'s commands run restricted by the operating system: they write only in the working directories, temp folders and toolchain caches, cannot read credential stores, and reach the network only through allowed hosts. Bypass mode skips permission prompts and leaves the sandbox as set here. Saved to your account, so it applies on every machine;') }} <code>sandbox.enabled</code> {{ tt('in a folder\'s') }} <code>.harness/settings.local.json</code> {{ tt('overrides it there.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <p
      v-if="unavailable"
      role="status"
      class="m-0 mb-3 border border-warn px-3 py-2 text-[12px] text-warn"
    >
      {{ tt('Not available on this machine: {0}. Commands run with your full rights.', [status?.reason]) }}
      <template v-if="required">
        {{ tt('Your administrator requires the sandbox, so commands are refused here.') }}
      </template>
    </p>
    <div class="set-row">
      <div class="set-label">
        <span>{{ tt('Run commands in the sandbox') }}</span>
        <span class="set-desc">
          <template v-if="required">{{ tt('Required by your administrator.') }}</template>
          <template v-else>{{ tt('Applies from the next command. Commands the model asks to run outside it go to you, every time.') }}</template>
        </span>
      </div>
      <HToggle
        v-model="enabled"
        :label="tt('Run commands in the sandbox')"
        :disabled="!loaded || unavailable || required"
      />
    </div>
    <div class="set-row">
      <label
        class="set-label"
        for="sandbox-write-paths"
      >
        <span>{{ tt('More write paths') }}</span>
        <span class="set-desc">{{ tt('One per line, absolute or starting with ~/. Protected paths (.git/hooks, shell startup files, ~/.ssh, harness settings) stay closed whatever this says.') }}</span>
      </label>
      <textarea
        id="sandbox-write-paths"
        v-model="paths"
        rows="3"
        spellcheck="false"
        class="w-64 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded || unavailable"
        @change="save({ allowedWritePaths: lines(paths) })"
      />
    </div>
    <div class="set-row">
      <label
        class="set-label"
        for="sandbox-hosts"
      >
        <span>{{ tt('More allowed hosts') }}</span>
        <span class="set-desc">{{ tt('One per line: example.com, *.example.com, either with :port. Always allowed: {0}.', [status?.builtinHosts.join(', ')]) }}</span>
      </label>
      <textarea
        id="sandbox-hosts"
        v-model="hosts"
        rows="3"
        spellcheck="false"
        class="w-64 border border-default bg-transparent px-2 py-1 text-[12.5px] text-strong"
        :disabled="!loaded || unavailable"
        @change="save({ allowedHosts: lines(hosts) })"
      />
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>{{ tt('Allow localhost') }}</span>
        <span class="set-desc">{{ tt('Commands may listen on local ports and reach what listens on this machine, which tests with a local server need. It also reaches every other local service. Preview servers always can.') }}</span>
      </div>
      <HToggle
        v-model="allowLocalhost"
        :label="tt('Allow localhost')"
        :disabled="!loaded || unavailable"
      />
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>{{ tt('Skip prompts for sandboxed commands') }}</span>
        <span class="set-desc">{{ tt('A command that would ask for permission runs in the sandbox without asking. Deny and ask rules still apply.') }}</span>
      </div>
      <HToggle
        v-model="autoAllowBash"
        :label="tt('Skip prompts for sandboxed commands')"
        :disabled="!loaded || unavailable"
      />
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>{{ tt('Run outside the sandbox without a special prompt') }}</span>
        <span class="set-desc">
          <template v-if="status?.outsideRefusedByPolicy">{{ tt('Your administrator does not allow running outside the sandbox.') }}</template>
          <template v-else>{{ tt('A command the model asks to run outside it gets the normal permission check, which bypass mode passes, instead of always asking you.') }}</template>
        </span>
      </div>
      <HToggle
        v-model="outsideWithoutPrompt"
        :label="tt('Run outside the sandbox without a special prompt')"
        :disabled="!loaded || unavailable || status?.outsideRefusedByPolicy"
      />
    </div>
  </section>
</template>
