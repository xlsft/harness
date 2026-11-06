<script setup lang="ts">
// Settings > SSH hosts: the machines a window can run on. Addresses only: ssh authenticates with the user's own agent and
// config, and this app never sees a key or a password. Kept on this device, never synced.
import type { SshHostVM } from '~/lib/ssh'

const store = useSessionStore()
const hosts = ref<SshHostVM[]>([])
const adding = ref(false)
const destination = ref('')
const name = ref('')
const port = ref('')
const identityFile = ref('')
const error = ref('')
const connecting = ref<SshHostVM | null>(null)

const message = (e: unknown) => e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)

async function load() {
  try {
    hosts.value = await store.sshApi?.list() ?? []
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(load)

async function add() {
  error.value = ''
  try {
    await store.sshApi?.save({ name: name.value.trim(), destination: destination.value.trim(), port: Number(port.value) || 0, identityFile: identityFile.value.trim() })
    destination.value = name.value = port.value = identityFile.value = ''
    adding.value = false
    await load()
  } catch (e) {
    error.value = message(e)
  }
}

async function remove(id: string) {
  error.value = ''
  try {
    await store.sshApi?.remove(id)
    await load()
  } catch (e) {
    error.value = message(e)
  }
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <section
    :aria-label="tt('SSH hosts')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('SSH hosts') }}
    </h2>
    <p class="text-[12px] text-muted">
      {{ tt('A window can run on another machine: its sessions, files and terminals are that machine\'s. Sign-in is your own ssh setup, with your agent and ~/.ssh/config. Nothing here is synced to your other devices.') }}
    </p>
    <ul
      v-if="hosts.length"
      class="mt-2 flex flex-col"
    >
      <li
        v-for="h in hosts"
        :key="h.id"
        class="flex items-center gap-2 border-b border-line py-1.5 text-[12.5px]"
      >
        <span class="min-w-0 flex-1 truncate text-strong">{{ h.name || h.destination }}</span>
        <span class="truncate text-muted">{{ h.destination }}{{ h.port ? `:${h.port}` : '' }}</span>
        <HBtn
          :disabled="store.sshHost?.hostId === h.id"
          @click="connecting = h"
        >
          {{ tt('Connect') }}
        </HBtn>
        <HBtn
          tone="danger"
          @click="remove(h.id)"
        >
          {{ tt('Remove') }}
        </HBtn>
      </li>
    </ul>
    <form
      v-if="adding"
      class="mt-3 grid gap-2 sm:grid-cols-2"
      :aria-label="tt('Add an SSH host')"
      @submit.prevent="add"
    >
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        {{ tt('Host') }}
        <input
          v-model="destination"
          required
          spellcheck="false"
          autocomplete="off"
          placeholder="alias or user@host"
          :class="input"
        >
      </label>
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        {{ tt('Name (optional)') }}
        <input
          v-model="name"
          :class="input"
        >
      </label>
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        {{ tt('Port (optional)') }}
        <input
          v-model="port"
          inputmode="numeric"
          :class="input"
        >
      </label>
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        {{ tt('Key file path (optional)') }}
        <input
          v-model="identityFile"
          spellcheck="false"
          placeholder="~/.ssh/id_ed25519"
          :class="input"
        >
      </label>
      <div class="flex gap-2 sm:col-span-2">
        <HBtn
          variant="primary"
          type="submit"
          :disabled="!destination.trim()"
        >
          {{ tt('Save') }}
        </HBtn>
        <HBtn @click="adding = false">
          {{ tt('Cancel') }}
        </HBtn>
      </div>
    </form>
    <div
      v-else
      class="mt-3"
    >
      <HBtn @click="adding = true">
        {{ tt('Add a host') }}
      </HBtn>
    </div>
    <p
      v-if="error"
      role="alert"
      class="mt-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <SshConnectModal
      v-if="connecting"
      :host="connecting"
      @close="connecting = null"
    />
  </section>
</template>
