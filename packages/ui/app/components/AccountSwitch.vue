<script setup lang="ts">
// Switch account (desktop): this device signs in to one account at a time, so switching signs out here and starts the
// device-flow sign-in again on the chosen server, right away. Accounts signed in on this device before are offered first;
// they are kept in this window's storage only (server and email, no tokens).
import { logout as desktopLogout, machineConfig } from '~/lib/desktop'
import { AUTO_SIGN_IN, LAST_SERVER, recentAccounts, type RecentAccount } from '~/lib/account/recent'

const open = defineModel<boolean>('open', { required: true })

const store = useSessionStore()
const recent = ref<RecentAccount[]>([])
const server = ref('')
const managed = ref(false)
const busy = ref(false)
const error = ref('')

const current = computed(() => store.account.server)
const others = computed(() => recent.value.filter(a => !(a.server === current.value && a.email === store.userEmail)))

function hostOf(url: string) {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

watch(open, async (v) => {
  if (!v) return
  error.value = ''
  busy.value = false
  recent.value = recentAccounts()
  server.value = current.value
  try {
    const machine = await machineConfig()
    if (machine.serverUrl) {
      server.value = machine.serverUrl
      managed.value = true
    }
  } catch {
    // An older shell: the user picks.
  }
}, { immediate: true })

async function switchTo(url: string) {
  const target = url.trim().replace(/\/+$/, '')
  if (!target || busy.value) return
  busy.value = true
  error.value = ''
  try {
    localStorage.setItem(LAST_SERVER, target)
    localStorage.setItem(AUTO_SIGN_IN, '1')
  } catch {
    // The sign-in screen then waits for a click instead.
  }
  try {
    await desktopLogout()
    window.location.reload()
  } catch (err) {
    busy.value = false
    error.value = err instanceof Error ? err.message : String(err)
  }
}
</script>

<template>
  <UModal
    v-model:open="open"
    :title="tt('Switch account')"
    :description="tt('You sign out here and confirm the new sign-in in your browser.')"
    :ui="{ content: 'max-w-[380px]' }"
  >
    <template #body>
      <div
        class="flex flex-col gap-3 text-[12.5px]"
        data-account-switch
      >
        <div
          v-if="others.length && !managed"
          class="flex flex-col border border-accented"
        >
          <button
            v-for="a in others"
            :key="a.server + a.email"
            type="button"
            :disabled="busy"
            class="flex items-center gap-2 px-2.5 py-2 text-left hover:bg-raised disabled:opacity-50"
            @click="switchTo(a.server)"
          >
            <HAvatar
              :name="a.name || a.email"
              :seed="a.email"
            />
            <span class="flex min-w-0 flex-1 flex-col">
              <span class="truncate text-strong">{{ a.name || a.email }}</span>
              <span class="truncate text-[11.5px] text-muted">{{ a.email }} · {{ hostOf(a.server) }}</span>
            </span>
          </button>
        </div>
        <form
          class="flex flex-col gap-1.5"
          @submit.prevent="switchTo(server)"
        >
          <label class="flex flex-col gap-1.5 text-[12px] text-muted">
            {{ others.length && !managed ? tt('Or another server') : tt('Server') }}
            <input
              v-model="server"
              type="url"
              spellcheck="false"
              autocomplete="url"
              :readonly="managed"
              :aria-label="tt('Server URL')"
              placeholder="https://harness.example.com"
              class="h-8 border border-accented bg-surface px-2.5 text-[12.5px] text-strong outline-none placeholder:text-dimmed focus:border-accent read-only:text-toned"
            >
            <span
              v-if="managed"
              class="text-[11.5px] text-muted"
            >{{ tt('Set by your administrator') }}</span>
          </label>
          <p
            v-if="error"
            role="alert"
            class="m-0 text-[12px] text-bad"
          >
            {{ error }}
          </p>
          <div class="mt-1 flex justify-end gap-2">
            <HBtn
              variant="ghost"
              size="md"
              @click="open = false"
            >
              {{ tt('Cancel') }}
            </HBtn>
            <HBtn
              type="submit"
              variant="primary"
              size="md"
              :disabled="busy || !server.trim()"
            >
              {{ busy ? tt('Signing out…') : tt('Continue') }}
            </HBtn>
          </div>
        </form>
      </div>
    </template>
  </UModal>
</template>
