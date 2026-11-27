<script setup lang="ts">
// Settings > About: what `harness-client doctor` says about the machine this client runs on. The client runs the checks
// (AgentService.RunDoctor); opening the page runs the quick ones, and the button also starts the MCP servers.
import type { DoctorCheckVM, DoctorReport, DoctorState } from '~/lib/doctor'

const store = useSessionStore()
const report = ref<DoctorReport | null>(null)
const running = ref(false)
const error = ref('')
const withMcp = ref(false)

async function run(mcp: boolean) {
  if (!store.transport || running.value) return
  running.value = true
  error.value = ''
  try {
    report.value = await store.transport.runDoctor({ skipMcp: !mcp })
    withMcp.value = mcp
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
  } finally {
    running.value = false
  }
}

onMounted(() => void run(false))

const TITLES: Record<string, string> = {
  signin: tt('Sign-in'),
  server: tt('Server'),
  version: tt('Version skew'),
  version_policy: tt('Version policy'),
  keychain: tt('Keychain'),
  sandbox: tt('Sandbox'),
  mcp: tt('MCP servers'),
  disk: tt('Disk space'),
  git: tt('git'),
  proxy: tt('Proxy')
}
const STATES: Record<DoctorState, { label: string, cls: string }> = {
  pass: { label: tt('Pass'), cls: 'text-ok' },
  warn: { label: tt('Warn'), cls: 'text-warn' },
  fail: { label: tt('Fail'), cls: 'text-bad' },
  skip: { label: tt('Skip'), cls: 'text-muted' }
}
const title = (c: DoctorCheckVM) => TITLES[c.id] ?? c.title
const problems = computed(() => report.value?.checks.filter(c => c.status === 'warn' || c.status === 'fail').length ?? 0)
</script>

<template>
  <section
    class="flex flex-col"
    :aria-label="tt('Health check')"
  >
    <div class="mb-2 flex items-center gap-3">
      <h3 class="m-0 flex-1 text-[11px] font-normal text-muted">
        {{ tt('Health check') }}
        <span
          v-if="report && !running"
          class="ml-1"
        >· {{ problems ? tt('{0} to look at', [problems]) : tt('all good') }}</span>
      </h3>
      <button
        type="button"
        class="text-[11.5px] text-muted hover:text-strong disabled:opacity-60"
        :disabled="running || !store.transport"
        @click="run(true)"
      >
        {{ running ? tt('Checking…') : withMcp ? tt('Check again') : tt('Also start MCP servers') }}
      </button>
    </div>
    <p
      v-if="error"
      role="alert"
      class="m-0 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <ul
      v-else-if="report"
      class="m-0 flex list-none flex-col overflow-hidden border border-default p-0"
    >
      <li
        v-for="c in report.checks"
        :key="c.id"
        class="flex items-start gap-3 border-b border-default px-4 py-2.5 last:border-b-0"
      >
        <span
          class="w-9 shrink-0 pt-px text-[11px]"
          :class="STATES[c.status].cls"
        >{{ STATES[c.status].label }}</span>
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <span class="text-[12.5px] text-strong">{{ title(c) }}</span>
          <span class="text-[11.5px] break-words text-muted">{{ c.detail }}</span>
          <span
            v-if="c.hint && (c.status === 'warn' || c.status === 'fail')"
            class="font-mono text-[11.5px] break-words text-toned"
          >{{ c.hint }}</span>
        </div>
      </li>
    </ul>
    <p
      v-else
      class="m-0 border border-default px-4 py-3 text-[11.5px] text-muted"
    >
      {{ store.transport ? tt('Checking…') : tt('Connect a client to check its machine.') }}
    </p>
  </section>
</template>
