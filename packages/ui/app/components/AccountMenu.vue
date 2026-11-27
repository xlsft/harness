<script setup lang="ts">
// The account button at the foot of the sidebar: avatar, name and server, and a menu with the settings you reach for
// most (account, usage, users for admins), the server in a browser, About, and signing out. Signing out asks first: the
// first click turns the row into a confirmation, the second signs out.
import { avatarUrl } from '~/lib/account/types'
import { isDesktop, logout as desktopLogout, openExternal } from '~/lib/desktop'

const emit = defineEmits<{ navigate: [] }>()

const store = useSessionStore()
const settings = useSettingsModal()
const toast = useToast()
const desktop = isDesktop()
const open = ref(false)
const confirmSignOut = ref(false)
watch(open, (v) => {
  if (!v) confirmSignOut.value = false
})

// The profile (name, avatar, role) from the server; the shell's whoami until it loads.
const { profile, load: loadProfile } = useAccountProfile()
onMounted(() => loadProfile())
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
      <img
        v-if="profile?.image"
        :src="avatarUrl(profile)"
        alt=""
        class="size-[22px] shrink-0 rounded-chip object-cover"
      >
      <span
        v-else
        class="flex size-[22px] shrink-0 items-center justify-center rounded-chip bg-accent text-[10.5px] font-bold text-on-accent uppercase"
      >{{ displayName.charAt(0) }}</span>
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
        </div>
        <div class="my-1 h-px bg-line" />
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
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          v-if="store.account.server"
          icon="arrow-up-right"
          @click="open = false; openExternal(store.account.server)"
        >
          {{ tt('Open the server in a browser') }}
        </PaneMenuItem>
        <PaneMenuItem
          icon="info"
          @click="openSettings('about')"
        >
          {{ tt('About and updates') }}
        </PaneMenuItem>
        <template v-if="desktop">
          <div class="my-1 h-px bg-line" />
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
