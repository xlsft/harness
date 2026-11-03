<script setup lang="ts">
// Replaces the composer for a session of another machine: where it runs, and the two ways to go on with it.
// "Continue via remote control" drives it through the relay (only while it is open on its machine and that machine
// is online); "Continue here" forks its transcript into a new session on this machine (the new-session screen
// picks the folder).
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()

const device = computed(() => props.session.meta.deviceName || 'another machine')
const presence = computed(() => {
  const m = props.session.meta
  if (m.remoteControllable) return { text: 'online', cls: 'text-ok' }
  if (m.deviceOnline === true) return { text: 'online · session not open there', cls: 'text-muted' }
  if (m.deviceOnline === false) return { text: 'offline', cls: 'text-muted' }
  return null
})
// Why remote control is unavailable (the disabled button's tooltip).
const blocked = computed(() => {
  const m = props.session.meta
  if (m.remoteControllable) return ''
  if (m.deviceOnline === false) return `${device.value} is offline`
  if (m.deviceOnline === true) return `Not open on ${device.value}: open it there to drive it from here`
  return 'Remote control is not available on this client'
})

function continueHere() {
  navigateTo({ path: '/', query: { new: '1', fork: props.session.meta.id } })
}
</script>

<template>
  <div
    role="status"
    class="flex flex-wrap items-center gap-x-3 gap-y-2 border border-line bg-raised px-3 py-2.5 text-[12.5px]"
  >
    <HIcon
      name="monitor"
      :size="14"
      class="shrink-0 text-muted"
    />
    <span class="min-w-0 truncate text-white">Running on {{ device }}</span>
    <span
      v-if="presence"
      class="text-[11.5px]"
      :class="presence.cls"
    >{{ presence.text }}</span>
    <span class="flex-1" />
    <HBtn
      size="sm"
      @click="continueHere"
    >
      <HIcon
        name="branch"
        :size="12"
      />Continue here
    </HBtn>
    <span :title="blocked || undefined">
      <HBtn
        size="sm"
        variant="primary"
        :disabled="!session.meta.remoteControllable"
        @click="store.continueRemotely(session.meta.id)"
      >
        <HIcon
          name="relay"
          :size="12"
        />Continue via remote control
      </HBtn>
    </span>
  </div>
</template>
