<script setup lang="ts">
// Environment picker of the new-session screen. A session runs on the client this window talks to: this computer, or a saved SSH
// host, which moves the whole window there. Or on an always-on host from the server's list (docs/design/hosts.md), which makes
// a checkout of its own: a host that can't take the work says why, and an offline one still takes it and the work waits.
import type { SshHostVM } from '~/lib/ssh'
import { hostLoadText, type HostView } from '~/lib/hosts/types'

const props = defineProps<{ agentHosts?: HostView[], current?: string }>()
const emit = defineEmits<{ close: [], pick: [hostId: string] }>()
const store = useSessionStore()
const query = ref('')
const hosts = ref<SshHostVM[]>([])
const connecting = ref<SshHostVM | null>(null)
const shown = computed(() => hosts.value.filter(h => `${h.name} ${h.destination}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const shownAgentHosts = computed(() => {
  const q = query.value.trim().toLowerCase()
  return (props.agentHosts ?? []).filter(h => !q || `${h.name} ${h.groupName}`.toLowerCase().includes(q))
})
const local = computed(() => !store.sshHost && !props.current)

onMounted(async () => {
  hosts.value = await store.sshApi?.list().catch(() => []) ?? []
})

function openSettings() {
  emit('close')
  void navigateTo({ path: '/', query: { settings: 'ssh' } })
}

function pickAgentHost(id: string) {
  emit('pick', id)
  emit('close')
}

function pickLocal() {
  emit('pick', '')
  if (store.sshHost) store.leaveSshHost()
  else emit('close')
}
const ROW = 'flex cursor-pointer items-center gap-2 border-l-2 px-2.5 py-[5px]'
</script>

<template>
  <div
    role="menu"
    :aria-label="tt('Environment')"
    class="w-[300px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
  >
    <label class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-muted">
      <HIcon
        name="search"
        :size="13"
      />
      <input
        v-model="query"
        :placeholder="tt('Search environments')"
        class="min-w-0 flex-1 bg-transparent text-strong outline-none placeholder:text-muted"
      >
    </label>
    <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
      {{ tt('Local') }}
    </div>
    <div
      role="menuitemradio"
      :aria-checked="local"
      :class="[ROW, local ? 'border-accent bg-raised text-strong' : 'border-transparent hover:bg-raised']"
      @click="pickLocal"
    >
      <HIcon
        name="monitor"
        :size="13"
      />
      <span class="flex-1">{{ tt('This computer') }}</span>
      <HIcon
        v-if="local"
        name="check"
        :size="13"
        class="text-accent"
      />
    </div>
    <template v-if="shownAgentHosts.length">
      <div class="my-1 h-px bg-line" />
      <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
        {{ tt('Always-on hosts') }}
      </div>
      <div
        v-for="h in shownAgentHosts"
        :key="h.id"
        role="menuitemradio"
        :aria-checked="current === h.id"
        :aria-disabled="!!h.unavailableReason"
        :title="h.unavailableReason || undefined"
        tabindex="0"
        :class="[ROW, current === h.id ? 'border-accent bg-raised text-strong' : 'border-transparent', h.unavailableReason ? 'cursor-not-allowed text-muted' : 'hover:bg-raised']"
        @click="!h.unavailableReason && pickAgentHost(h.id)"
        @keydown.enter="!h.unavailableReason && pickAgentHost(h.id)"
      >
        <HIcon
          name="box"
          :size="13"
        />
        <span class="min-w-0 flex-1 truncate">{{ h.name }}<template v-if="h.groupName"> · {{ h.groupName }}</template></span>
        <span class="shrink-0 text-[11px] text-muted">{{ h.unavailableReason ? tt('unavailable') : hostLoadText(h) }}</span>
        <HIcon
          v-if="current === h.id"
          name="check"
          :size="13"
          class="text-accent"
        />
      </div>
    </template>
    <div class="my-1 h-px bg-line" />
    <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
      {{ tt('SSH') }}
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
      <span class="flex-1">{{ tt('Add an SSH host…') }}</span>
    </div>
    <SshConnectModal
      v-if="connecting"
      :host="connecting"
      @close="connecting = null"
    />
  </div>
</template>
