<script setup lang="ts">
// Settings page: sections on the left, the chosen one on the right (?section=). General edits the defaults the
// new-session screen starts from (the same localStorage key it remembers its last choices in).
import { isDesktop, logout as desktopLogout } from '~/lib/desktop'

const store = useSessionStore()
const route = useRoute()
const router = useRouter()
const desktop = isDesktop()

const SECTIONS = [
  { key: 'general', label: 'General', icon: 'sliders' },
  { key: 'models', label: 'Models', icon: 'ai' },
  { key: 'classifiers', label: 'Classifiers', icon: 'shield' },
  { key: 'account', label: 'Account', icon: 'lock' },
  { key: 'users', label: 'Users', icon: 'shield' },
  { key: 'about', label: 'About', icon: 'info' }
] as const
type Section = typeof SECTIONS[number]['key']

// Users is for admins only (the server enforces it too). The web has no role from the shell: ask the server.
const isAdmin = ref(store.account.role === 'admin')
onMounted(async () => {
  try {
    if (store.transport) isAdmin.value = await store.transport.users().isAdmin()
  } catch {
    // Keep what the shell said.
  }
})
const sections = computed(() => SECTIONS.filter(s => s.key !== 'users' || isAdmin.value))

const section = computed<Section>({
  get: () => sections.value.find(s => s.key === route.query.section)?.key ?? 'general',
  set: v => router.replace({ query: { ...route.query, section: v === 'general' ? undefined : v } })
})

// New-session defaults, shared with NewSession.vue.
const { prefs: defaults, save } = useSessionDefaults()
const prefs = computed(() => defaults.value)
const worktree = computed({ get: () => prefs.value.worktree ?? false, set: v => save('worktree', v) })

const host = computed(() => {
  try {
    return store.account.server ? new URL(store.account.server).host : ''
  } catch {
    return store.account.server
  }
})

async function signOut() {
  await desktopLogout()
  window.location.reload()
}
</script>

<template>
  <section
    aria-label="Settings"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[960px] gap-10 px-10 pt-2 pb-10 max-md:flex-col max-md:gap-5 max-sm:px-4 max-sm:pt-6">
      <nav
        aria-label="Settings sections"
        class="sticky top-2 flex w-[180px] shrink-0 flex-col gap-[2px] self-start max-md:static max-md:w-full max-md:flex-row max-md:flex-wrap"
      >
        <h1 class="m-0 mb-4 font-display text-[26px] font-bold text-white max-md:mb-1 max-md:w-full">
          Settings
        </h1>
        <button
          v-for="s in sections"
          :key="s.key"
          type="button"
          class="nav-row px-[9px] hover:bg-hover"
          :class="section === s.key ? 'nav-row-current' : ''"
          :aria-current="section === s.key ? 'page' : undefined"
          @click="section = s.key"
        >
          <HIcon :name="s.icon" />{{ s.label }}
        </button>
      </nav>

      <div class="flex min-w-0 flex-1 flex-col">
        <template v-if="section === 'general'">
          <SettingsModes />
          <h2 class="set-h">
            New sessions
          </h2>
          <p class="set-sub">
            What the new-session screen starts with. Changing a chip there updates these too.
          </p>
          <div class="set-row">
            <div class="set-label">
              <span>Worktree</span>
              <span class="set-desc">Run each new session in its own git worktree.</span>
            </div>
            <HToggle
              v-model="worktree"
              label="Worktree by default"
            />
          </div>
        </template>

        <SettingsProviders
          v-else-if="section === 'models' || section === 'classifiers'"
          :key="section"
          :group="section === 'models' ? 'llm' : 'classifier'"
        />

        <template v-else-if="section === 'users'">
          <SettingsUsers />
          <SettingsLdap />
        </template>

        <template v-else-if="section === 'account'">
          <h2 class="set-h">
            Account
          </h2>
          <p class="set-sub">
            Who you are signed in as, and where.
          </p>
          <div class="set-row">
            <div class="set-label">
              <span>Email</span>
            </div>
            <span class="text-[12.5px] text-white">{{ store.userEmail || 'Not signed in' }}</span>
          </div>
          <div
            v-if="store.account.name"
            class="set-row"
          >
            <div class="set-label">
              <span>Name</span>
            </div>
            <span class="text-[12.5px] text-white">{{ store.account.name }}</span>
          </div>
          <div
            v-if="host"
            class="set-row"
          >
            <div class="set-label">
              <span>Server</span>
            </div>
            <span class="text-[12.5px] text-white">{{ host }}</span>
          </div>
          <div
            v-if="store.account.role"
            class="set-row"
          >
            <div class="set-label">
              <span>Role</span>
            </div>
            <span class="text-[12.5px] text-white capitalize">{{ store.account.role }}</span>
          </div>
          <div
            v-if="desktop"
            class="set-row"
          >
            <div class="set-label">
              <span>Sign out</span>
              <span class="set-desc">Stops the agent on this computer and forgets its credentials.</span>
            </div>
            <button
              type="button"
              class="h-8 border border-bad px-3 text-[12.5px] text-bad hover:bg-bad hover:text-black"
              @click="signOut"
            >
              Sign out
            </button>
          </div>
        </template>

        <SettingsAbout v-else />
      </div>
    </div>
  </section>
</template>
