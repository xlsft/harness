<script setup lang="ts">
// The account button at the foot of the sidebar: avatar, name and server, and a menu with the settings you reach for
// most (account, usage, users for admins), the server in a browser, About, then (desktop) the accounts this device was
// signed in to, a server to type, and signing out. This device signs in to one account at a time, so choosing another
// signs out here and starts the sign-in on that server at once. Signing out asks first: the first click turns the row
// into a confirmation, the second signs out.
import { avatarUrl } from '~/lib/account/types'
import { isDesktop, logout as desktopLogout, machineConfig, openExternal } from '~/lib/desktop'
import { AUTO_SIGN_IN, LAST_SERVER, recentAccounts, rememberAccount, type RecentAccount } from '~/lib/account/recent'

const emit = defineEmits<{ navigate: [] }>()

const store = useSessionStore()
const settings = useSettingsModal()
const toast = useToast()
const desktop = isDesktop()
const open = ref(false)
const confirmSignOut = ref(false)
const accounts = ref<RecentAccount[]>([])
const server = ref('')
// A server the administrator set for this machine: no other can be chosen.
const managed = ref(false)
const switchError = ref('')
const busy = ref(false)
watch(open, async (v) => {
  if (!v) {
    confirmSignOut.value = false
    return
  }
  switchError.value = ''
  server.value = ''
  if (!desktop) return
  accounts.value = recentAccounts()
  try {
    managed.value = !!(await machineConfig()).serverUrl
  } catch {
    managed.value = false
  }
})
const isCurrent = (a: RecentAccount) => a.server === store.account.server && a.email === store.userEmail
const hostOf = (url: string) => {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

// The profile (name, avatar, role) from the server; the shell's whoami until it loads.
const { profile, load: loadProfile } = useAccountProfile()
onMounted(() => loadProfile())
// Switch account offers the accounts this device was signed in to.
watchEffect(() => {
  if (desktop && store.account.server && store.userEmail) rememberAccount({ server: store.account.server, email: store.userEmail, name: profile.value?.name || store.account.name || '' })
})
const displayName = computed(() => profile.value?.name || store.account.name || store.userEmail.split('@')[0] || tt('Not signed in'))
const isAdmin = computed(() => (profile.value?.role || store.account.role) === 'admin')
const host = computed(() => {
  try {
    return store.account.server ? new URL(store.account.server).host : ''
  } catch {
    return store.account.server
  }
})

function openSettings(section?: string) {
  open.value = false
  emit('navigate')
  settings.open(section)
}

async function switchTo(url: string) {
  const target = url.trim().replace(/\/+$/, '')
  if (!target || busy.value) return
  busy.value = true
  switchError.value = ''
  try {
    localStorage.setItem(LAST_SERVER, /^https?:\/\//.test(target) ? target : `https://${target}`)
    localStorage.setItem(AUTO_SIGN_IN, '1')
  } catch {
    // The sign-in screen then waits for a click instead.
  }
  try {
    await desktopLogout()
    window.location.reload()
  } catch (err) {
    busy.value = false
    switchError.value = err instanceof Error ? err.message : String(err)
  }
}

async function signOut() {
  if (!confirmSignOut.value) {
    confirmSignOut.value = true
    return
  }
  open.value = false
  try {
    await desktopLogout()
    window.location.reload()
  } catch (err) {
    toast.add({ title: tt('Could not sign out'), description: err instanceof Error ? err.message : String(err), color: 'error' })
  }
}
</script>

<template>
  <UPopover
    v-model:open="open"
    :content="{ side: 'top', align: 'start', sideOffset: 6 }"
  >
    <button
      type="button"
      :aria-label="tt('Account menu')"
      :title="store.userEmail"
      class="-ml-1.5 flex h-7 min-w-0 cursor-pointer items-center gap-2 px-1.5 text-left hover:bg-hover"
      :class="open ? 'bg-hover' : ''"
    >
      <HAvatar
        :src="avatarUrl(profile)"
        :name="displayName"
        :seed="profile?.username || store.userEmail"
      />
      <span class="flex min-w-0 items-baseline gap-1.5">
        <!-- The host gives way first, then the name. -->
        <span class="min-w-[4ch] truncate text-[12px] text-strong">{{ displayName }}</span>
        <span
          v-if="host"
          class="min-w-0 shrink-[9999] truncate text-[10.5px] text-muted"
        >{{ host }}</span>
      </span>
      <HIcon
        name="down"
        :size="11"
        class="shrink-0 text-muted"
      />
    </button>
    <template #content>
      <div
        role="menu"
        :aria-label="tt('Account')"
        class="w-[240px] border border-accented bg-hover py-1 text-[12.5px]"
      >
        <div class="flex flex-col gap-0.5 px-[9px] pt-1 pb-2">
          <span class="truncate text-strong">{{ displayName }}</span>
          <span
            v-if="store.userEmail"
            class="truncate text-[11.5px] text-muted"
          >{{ store.userEmail }}</span>
          <span
            v-if="host"
            class="truncate text-[11.5px] text-muted"
          >{{ host }}</span>
          <span
            v-if="profile?.groups?.length"
            class="mt-1 flex flex-wrap gap-1"
          >
            <HGroupChip
              v-for="g in profile.groups"
              :key="g"
              :name="g"
            />
          </span>
        </div>
        <div class="my-1 h-px bg-line-strong" />
        <PaneMenuItem
          icon="lock"
          @click="openSettings('account')"
        >
          {{ tt('Account') }}
        </PaneMenuItem>
        <PaneMenuItem
          icon="chart"
          @click="openSettings('usage')"
        >
          {{ tt('Usage') }}
        </PaneMenuItem>
        <PaneMenuItem
          v-if="isAdmin"
          icon="list"
          @click="openSettings('users')"
        >
          {{ tt('Users') }}
        </PaneMenuItem>
        <PaneMenuItem
          icon="settings"
          kbd="meta+,"
          @click="openSettings()"
        >
          {{ tt('Settings') }}
        </PaneMenuItem>
        <div class="my-1 h-px bg-line-strong" />
        <PaneMenuItem
          v-if="store.account.server"
          icon="arrow-up-right"
          @click="open = false; openExternal(store.account.server)"
        >
          {{ tt('Open in browser') }}
        </PaneMenuItem>
        <PaneMenuItem
          icon="info"
          @click="openSettings('about')"
        >
          {{ tt('About and updates') }}
        </PaneMenuItem>
        <template v-if="desktop">
          <div class="my-1 h-px bg-line-strong" />
          <div
            v-if="!managed && accounts.length"
            role="group"
            :aria-label="tt('Accounts')"
          >
            <button
              v-for="a in accounts"
              :key="a.server + a.email"
              type="button"
              role="menuitemradio"
              :aria-checked="isCurrent(a)"
              :disabled="busy || isCurrent(a)"
              class="flex w-full items-center gap-2 px-[9px] py-1.5 text-left enabled:hover:bg-raised disabled:cursor-default"
              @click="switchTo(a.server)"
            >
              <HAvatar
                :name="a.name || a.email"
                :seed="a.email"
              />
              <span class="flex min-w-0 flex-1 flex-col">
                <span class="truncate text-strong">{{ a.name || a.email }}</span>
                <span class="truncate text-[11px] text-muted">{{ a.name ? `${a.email} · ${hostOf(a.server)}` : hostOf(a.server) }}</span>
              </span>
              <HIcon
                v-if="isCurrent(a)"
                name="check"
                :size="14"
                class="shrink-0 text-accent"
              />
            </button>
          </div>
          <form
            v-if="!managed"
            class="px-[9px] py-1"
            @submit.prevent="switchTo(server)"
          >
            <input
              v-model="server"
              type="text"
              inputmode="url"
              spellcheck="false"
              autocomplete="url"
              :disabled="busy"
              :aria-label="tt('Sign in to another server')"
              :placeholder="tt('Another server, then Enter')"
              class="h-7 w-full border border-accented bg-surface px-2 text-[12px] text-strong outline-none placeholder:text-dimmed focus:border-accent"
            >
          </form>
          <p
            v-if="switchError"
            role="alert"
            class="m-0 px-[9px] py-1 text-[11.5px] text-bad"
          >
            {{ switchError }}
          </p>
          <PaneMenuItem
            icon="arrow-left"
            danger
            @click="signOut"
          >
            {{ confirmSignOut ? tt('Sign out? Click again') : tt('Sign out') }}
          </PaneMenuItem>
        </template>
      </div>
    </template>
  </UPopover>
</template>
