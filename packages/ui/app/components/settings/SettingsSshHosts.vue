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
    <p class="set-sub">
      {{ tt('A window can run on another machine: its sessions, files and terminals are that machine\'s. Sign-in is your own ssh setup, with your agent and ~/.ssh/config. Nothing here is synced to your other devices.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div class="mb-8 overflow-hidden border border-default text-[12.5px]">
      <div
        v-for="h in hosts"
        :key="h.id"
        class="flex items-center gap-3 border-b border-default px-3 py-2.5"
      >
        <SettingsMonogram
          icon="terminal"
          :dim="store.sshHost?.hostId !== h.id"
        />
        <span class="flex min-w-0 flex-1 flex-col gap-0.5">
          <span class="flex items-center gap-2 text-[12.5px] text-strong">
            {{ h.name || h.destination }}
            <span
              v-if="store.sshHost?.hostId === h.id"
              class="text-[11px] text-ok"
            >{{ tt('connected') }}</span>
          </span>
          <span class="truncate text-[11.5px] text-muted">{{ h.destination }}{{ h.port ? `:${h.port}` : '' }}</span>
        </span>
        <HBtn
          size="sm"
          :disabled="store.sshHost?.hostId === h.id"
          @click="connecting = h"
        >
          {{ tt('Connect') }}
        </HBtn>
        <HBtn
          size="sm"
          tone="danger"
          :aria-label="tt('Remove {0}', [h.name || h.destination])"
          @click="remove(h.id)"
        >
          {{ tt('Remove') }}
        </HBtn>
      </div>

      <div class="flex flex-col">
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            icon="plus"
            :dim="!hosts.length"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="text-[12.5px] text-strong">{{ tt('Add a machine') }}</span>
            <span class="truncate text-[11.5px] text-muted">{{ tt('An alias from ~/.ssh/config or user@host, signed in with your own ssh setup.') }}</span>
          </span>
          <HBtn
            v-if="!adding"
            size="sm"
            @click="adding = true"
          >
            <HIcon
              name="plus"
              :size="12"
            />{{ tt('Add') }}
          </HBtn>
        </div>
        <form
          v-if="adding"
          class="grid gap-2 border-t border-default py-3 pr-3 pl-[52px] sm:grid-cols-2"
          :aria-label="tt('Add an SSH host')"
          @submit.prevent="add"
        >
          <label class="flex flex-col gap-1 text-[11.5px] text-muted">
            {{ tt('Machine') }}
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
              size="sm"
              type="submit"
              :disabled="!destination.trim()"
            >
              {{ tt('Save') }}
            </HBtn>
            <HBtn
              size="sm"
              @click="adding = false"
            >
              {{ tt('Cancel') }}
            </HBtn>
          </div>
        </form>
      </div>
    </div>
    <SshConnectModal
      v-if="connecting"
      :host="connecting"
      @close="connecting = null"
    />
  </section>
</template>
