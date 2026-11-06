<script setup lang="ts">
// This window runs on an SSH host: says so, and offers the way back. Also says when the connection dropped: then nothing here
// answers, and the choice is to connect again or go back to this computer.
const store = useSessionStore()
const host = computed(() => store.sshHost)
let poll: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  poll = setInterval(() => store.checkSshConnection(), 5000)
})
onBeforeUnmount(() => clearInterval(poll))
</script>

<template>
  <div
    v-if="host"
    class="fixed right-3 bottom-3 z-30 flex h-8 max-w-[min(520px,calc(100vw-24px))] items-center gap-2 rounded-box border border-line-strong bg-raised px-3 text-[12px]"
    :class="store.sshLost ? 'text-bad' : 'text-muted'"
    :role="store.sshLost ? 'alert' : 'status'"
  >
    <HIcon
      name="terminal"
      :size="13"
    />
    <span class="min-w-0 flex-1 truncate">{{ store.sshLost ? tt('The connection to {0} was lost.', [host.name]) : tt('Running on {0} over SSH', [host.name]) }}</span>
    <HBtn
      v-if="store.sshLost"
      @click="store.reconnectSshHost()"
    >
      {{ tt('Connect again') }}
    </HBtn>
    <HBtn @click="store.leaveSshHost()">
      {{ tt('Back to this computer') }}
    </HBtn>
  </div>
</template>
