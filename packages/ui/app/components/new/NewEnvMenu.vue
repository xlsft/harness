<script setup lang="ts">
// Environment picker of the new-session screen. A session runs on the client this window talks to: this computer, or a saved SSH
// host, which moves the whole window there. Remote-control hosts (RelayService) are listed here once the UI talks to core.
import type { SshHostVM } from '~/lib/ssh'

const emit = defineEmits<{ close: [] }>()
const store = useSessionStore()
const query = ref('')
const hosts = ref<SshHostVM[]>([])
const connecting = ref<SshHostVM | null>(null)
const shown = computed(() => hosts.value.filter(h => `${h.name} ${h.destination}`.toLowerCase().includes(query.value.trim().toLowerCase())))

onMounted(async () => {
  hosts.value = await store.sshApi?.list().catch(() => []) ?? []
})

function openSettings() {
  emit('close')
  void navigateTo({ path: '/', query: { settings: 'ssh' } })
}
const ROW = 'flex cursor-pointer items-center gap-2 border-l-2 px-2.5 py-[5px]'
</script>

<template>
  <div
    role="menu"
    aria-label="Environment"
    class="w-[280px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
  >
    <label class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-muted">
      <HIcon
        name="search"
        :size="13"
      />
      <input
        v-model="query"
        placeholder="Search environments"
        class="min-w-0 flex-1 bg-transparent text-strong outline-none placeholder:text-muted"
      >
    </label>
    <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
      Local
    </div>
    <div
      role="menuitemradio"
      :aria-checked="!store.sshHost"
      :class="[ROW, store.sshHost ? 'border-transparent' : 'border-accent bg-raised text-strong']"
      @click="store.sshHost ? store.leaveSshHost() : emit('close')"
    >
      <HIcon
        name="monitor"
        :size="13"
      />
      <span class="flex-1">This computer</span>
      <HIcon
        v-if="!store.sshHost"
        name="check"
        :size="13"
        class="text-accent"
      />
    </div>
    <div class="my-1 h-px bg-line" />
    <div
      role="menuitem"
      aria-disabled="true"
      :class="[ROW, 'border-transparent text-muted']"
    >
      <HIcon
        name="relay"
        :size="13"
      />
      <span class="flex-1">Remote control</span>
      <span class="text-[11px]">soon</span>
    </div>
    <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
      SSH
    </div>
    <div
      v-for="h in shown"
      :key="h.id"
      role="menuitemradio"
      :aria-checked="store.sshHost?.hostId === h.id"
      tabindex="0"
      :class="[ROW, store.sshHost?.hostId === h.id ? 'border-accent bg-raised text-strong' : 'border-transparent hover:bg-raised']"
      @click="store.sshHost?.hostId === h.id ? emit('close') : connecting = h"
      @keydown.enter="store.sshHost?.hostId === h.id ? emit('close') : connecting = h"
    >
      <HIcon
        name="terminal"
        :size="13"
      />
      <span class="min-w-0 flex-1 truncate">{{ h.name || h.destination }}</span>
    </div>
    <div
      role="menuitem"
      tabindex="0"
      :class="[ROW, 'border-transparent text-muted hover:bg-raised']"
      @click="openSettings"
      @keydown.enter="openSettings"
    >
      <HIcon
        name="plus"
        :size="13"
      />
      <span class="flex-1">Add an SSH host…</span>
    </div>
    <SshConnectModal
      v-if="connecting"
      :host="connecting"
      @close="connecting = null"
    />
  </div>
</template>
