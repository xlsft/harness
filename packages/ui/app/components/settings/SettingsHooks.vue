<script setup lang="ts">
// Settings > Hooks: the switch that turns hooks off (a user preference, synced) and a read-only list of what is configured for
// the open session or the levels that do not depend on a folder. The client works out whether each runs; trusting what the
// server delivered is a choice of this device and never syncs.
import type { ConfiguredHookVM } from '~/lib/transport/types'

const store = useSessionStore()

const hooks = ref<ConfiguredHookVM[]>([])
const enabled = ref(true)
const loaded = ref(false)
const error = ref('')
const trusting = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

// Sessions of an external agent run no hooks of ours: their folder's list says what a session of this client would run.
const session = computed(() => store.current?.meta)
const scope = computed(() => session.value?.cwd
  ? `What runs in ${session.value.cwd}. Files changed since the session opened apply from its next start.`
  : 'What runs whatever the folder. Open a session to see the hooks of its project too.')

async function refresh() {
  if (!store.transport) return
  hooks.value = await store.transport.listHooks({ sessionId: session.value?.id, cwd: session.value?.cwd })
}

onMounted(async () => {
  if (!store.transport) return
  try {
    enabled.value = (await store.transport.getUserPreferences()).hooksEnabled
    await refresh()
    loaded.value = true
  } catch (e) {
    error.value = message(e)
  }
})

const value = computed({
  get: () => enabled.value,
  set: v => void save(v)
})

async function save(v: boolean) {
  if (!store.transport) return
  const prev = enabled.value
  enabled.value = v
  try {
    enabled.value = (await store.transport.updateUserPreferences({ hooksEnabled: v })).hooksEnabled
    error.value = ''
    await refresh()
  } catch (e) {
    enabled.value = prev
    error.value = message(e)
  }
}

async function trust(h: ConfiguredHookVM) {
  if (!store.transport) return
  trusting.value = h.trustHash
  try {
    await store.transport.setHooksTrust(h.trustHash, true)
    error.value = ''
    await refresh()
  } catch (e) {
    error.value = message(e)
  } finally {
    trusting.value = ''
  }
}

const STATUS_LABEL: Record<ConfiguredHookVM['status'], string> = {
  active: 'Runs',
  disabled: 'Off',
  needs_workspace_trust: 'Folder not trusted',
  needs_device_trust: 'Not trusted here',
  blocked_by_policy: 'Blocked by policy',
  unsupported: 'Does not run'
}

function statusClass(h: ConfiguredHookVM) {
  if (h.status === 'active') return 'text-ok'
  return h.status === 'disabled' ? 'text-muted' : 'text-warn'
}
</script>

<template>
  <section
    :aria-label="tt('Hooks')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Hooks') }}
    </h2>
    <p class="set-sub">
      {{ tt('Programs your settings run at points of a session: they can refuse a tool call, rewrite its input, add context or keep the agent working. They are defined under') }} <code>hooks</code> {{ tt('in') }} <code>.claude/settings*.json</code>, <code>.harness/settings*.json</code> {{ tt('and your user settings, and run with your own rights, outside the sandbox. Sessions of an external ACP agent run none of these: that agent runs its own.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <div class="set-row">
      <div class="set-label">
        <span>{{ tt('Run hooks') }}</span>
        <span class="set-desc">{{ tt('Saved to your account, so it applies on every machine, and to running sessions from their next hook. Hooks in an organization policy keep running.') }}</span>
      </div>
      <HToggle
        v-model="value"
        :label="tt('Run hooks')"
        :disabled="!loaded"
      />
    </div>
    <h3 class="mt-5 mb-1 text-[12.5px] text-strong">
      {{ tt('Configured') }}
    </h3>
    <p class="set-sub">
      {{ scope }}
    </p>
    <p
      v-if="loaded && !hooks.length"
      class="m-0 text-[12px] text-muted"
    >
      {{ tt('No hooks are configured.') }}
    </p>
    <ul
      v-else
      class="m-0 flex list-none flex-col p-0"
    >
      <li
        v-for="(h, i) in hooks"
        :key="i"
        class="set-row"
        data-hook-row
      >
        <div class="set-label min-w-0">
          <span>{{ h.event }}<template v-if="h.matcher">
            · {{ h.matcher }}
          </template></span>
          <span class="set-desc break-all"><code>{{ h.command || h.handlerType }}</code></span>
          <span class="set-desc">
            {{ tt('{0} · {1} settings', [h.handlerType, h.source]) }}<template v-if="h.sourcePath">
              ({{ h.sourcePath }})
            </template><template v-if="h.timeoutSeconds">
              {{ tt('· {0} s', [h.timeoutSeconds]) }}
            </template><template v-if="h.async">
              {{ tt('· on its own') }}
            </template>
          </span>
          <span
            v-if="h.reason"
            class="set-desc"
          >{{ h.reason }}</span>
        </div>
        <div class="flex shrink-0 items-center gap-2 text-[12px]">
          <span :class="statusClass(h)">{{ STATUS_LABEL[h.status] }}</span>
          <HBtn
            v-if="h.status === 'needs_device_trust'"
            size="sm"
            :disabled="trusting === h.trustHash"
            @click="trust(h)"
          >
            {{ tt('Trust on this machine') }}
          </HBtn>
        </div>
      </li>
    </ul>
  </section>
</template>
