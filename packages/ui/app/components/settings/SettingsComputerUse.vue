<script setup lang="ts">
// Settings > Computer use: the switch and what the operating system allows. Device-local: kept by the client of this
// machine and never synced, so each device of the user decides for itself. Off by default. The status is only read,
// which never makes the system prompt; the user grants permissions in the system's settings.
import type { ComputerUseStatusVM } from '~/lib/transport/types'

const store = useSessionStore()
const status = ref<ComputerUseStatusVM | null>(null)
const error = ref('')
const saving = ref(false)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load() {
  if (!store.transport) return
  try {
    status.value = await store.transport.getComputerUseStatus()
    error.value = ''
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(load)

const enabled = computed({
  get: () => status.value?.enabled ?? false,
  set: v => void save(v)
})

async function save(v: boolean) {
  if (!store.transport || saving.value) return
  saving.value = true
  try {
    status.value = await store.transport.setComputerUseEnabled(v)
    error.value = ''
  } catch (e) {
    error.value = message(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section
    aria-label="Computer use"
    class="flex flex-col"
  >
    <h2 class="set-h">
      Computer use
    </h2>
    <p class="set-sub">
      Lets the agent see and control apps on this machine. You approve each app in a session, and can stop it at any time. This setting applies to this device only.
    </p>
    <p
      v-if="error"
      role="alert"
      class="mb-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <div class="set-row">
      <div class="set-label">
        <span>Allow computer use on this device</span>
        <span class="set-desc">Browsers are read-only. Terminals and IDEs take clicks only. Password managers and system security settings are never controllable.</span>
      </div>
      <HToggle
        v-model="enabled"
        label="Allow computer use on this device"
        :disabled="!status || !status.supported || saving"
      />
    </div>
    <p
      v-if="status?.unavailableReason"
      class="mt-2 text-[12px] text-muted"
    >
      {{ status.unavailableReason }}
    </p>
    <div
      v-for="p in status?.permissions ?? []"
      :key="p.id"
      class="set-row"
    >
      <div class="set-label">
        <span>{{ p.name }}</span>
        <span
          v-if="!p.granted"
          class="set-desc"
        >{{ p.howToGrant }}</span>
      </div>
      <span
        class="text-[12px]"
        :class="p.granted ? 'text-ok' : 'text-warn'"
      >{{ p.granted ? 'Allowed' : 'Not allowed' }}</span>
    </div>
    <div
      v-if="status?.permissions?.length"
      class="mt-2"
    >
      <HBtn @click="load">
        Check again
      </HBtn>
    </div>
  </section>
</template>
