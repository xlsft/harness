<script setup lang="ts">
// Settings (the body of SettingsModal): sections on the left with a filter, the chosen one on the right
// (?settings=<section>). General edits the defaults the new-session screen starts from (the same localStorage key
// it remembers its last choices in).
import { isDesktop, logout as desktopLogout } from '~/lib/desktop'

const store = useSessionStore()
const modal = useSettingsModal()
const desktop = isDesktop()

const SECTIONS = [
  { key: 'general', label: 'General', icon: 'sliders' },
  { key: 'models', label: 'Models', icon: 'ai' },
  { key: 'classifiers', label: 'Classifiers', icon: 'shield' },
  { key: 'search', label: 'Search', icon: 'search' },
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
// What each section holds, so the filter finds "theme" under General and "LDAP" under Users.
const KEYWORDS: Record<Section, string> = {
  general: 'appearance theme light dark mode permission modes new sessions worktree',
  models: 'providers llm keys slots default model title effort',
  classifiers: 'classifier decision model rules',
  search: 'web search providers searxng brave tavily exa kagi perplexity',
  account: 'sign out server user',
  users: 'admin users ldap roles',
  about: 'version about'
}
const query = ref('')
const allowed = computed(() => SECTIONS.filter(s => s.key !== 'users' || isAdmin.value))
const sections = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? allowed.value.filter(s => `${s.label} ${KEYWORDS[s.key]}`.toLowerCase().includes(q)) : allowed.value
})

const section = computed<Section>({
  get: () => allowed.value.find(s => s.key === modal.section.value)?.key ?? 'general',
  set: v => modal.setSection(v)
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
  <div class="flex min-h-0 flex-1 max-md:flex-col">
    <nav
      aria-label="Settings sections"
      class="flex w-[200px] shrink-0 flex-col gap-[2px] overflow-y-auto border-r border-line p-3 max-md:w-full max-md:flex-row max-md:flex-wrap max-md:border-r-0 max-md:border-b"
    >
      <label class="mb-2 flex h-8 items-center gap-2 border border-line px-2.5 text-muted focus-within:border-accent max-md:w-full">
        <HIcon
          name="search"
          :size="13"
        />
        <input
          v-model="query"
          placeholder="Search"
          aria-label="Search settings"
          class="min-w-0 flex-1 bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
        >
      </label>
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
      <p
        v-if="!sections.length"
        class="m-0 px-[9px] py-2 text-[12px] text-muted"
      >
        Nothing matches.
      </p>
    </nav>

    <div class="min-w-0 flex-1 overflow-y-auto px-7 pt-5 pb-8 max-sm:px-4">
      <template v-if="section === 'general'">
        <SettingsAppearance />
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

      <SettingsSearch v-else-if="section === 'search'" />

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
          <span class="text-[12.5px] text-strong">{{ store.userEmail || 'Not signed in' }}</span>
        </div>
        <div
          v-if="store.account.name"
          class="set-row"
        >
          <div class="set-label">
            <span>Name</span>
          </div>
          <span class="text-[12.5px] text-strong">{{ store.account.name }}</span>
        </div>
        <div
          v-if="host"
          class="set-row"
        >
          <div class="set-label">
            <span>Server</span>
          </div>
          <span class="text-[12.5px] text-strong">{{ host }}</span>
        </div>
        <div
          v-if="store.account.role"
          class="set-row"
        >
          <div class="set-label">
            <span>Role</span>
          </div>
          <span class="text-[12.5px] text-strong capitalize">{{ store.account.role }}</span>
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
            class="h-8 border border-bad px-3 text-[12.5px] text-bad hover:bg-bad hover:text-on-accent"
            @click="signOut"
          >
            Sign out
          </button>
        </div>
      </template>

      <SettingsAbout v-else />
    </div>
  </div>
</template>
