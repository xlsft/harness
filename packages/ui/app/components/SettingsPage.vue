<script setup lang="ts">
// Settings (the body of SettingsModal): sections on the left with a filter, the chosen one on the right
// (?settings=<section>). General edits the defaults the new-session screen starts from (the same localStorage key
// it remembers its last choices in).

const store = useSessionStore()
const modal = useSettingsModal()

// Grouped like the nav shows them: you, this app, the AI services, the server (admins), the product.
const SECTIONS = [
  { key: 'account', label: 'Account', icon: 'lock', group: 'you' },
  { key: 'usage', label: 'Usage', icon: 'chart', group: 'you' },
  { key: 'appearance', label: 'Appearance', icon: 'palette', group: 'app' },
  { key: 'sessions', label: 'Sessions', icon: 'session', group: 'app' },
  { key: 'sandbox', label: 'Sandbox', icon: 'lock', group: 'app' },
  { key: 'import', label: 'Import', icon: 'download', group: 'app' },
  { key: 'models', label: 'Models', icon: 'ai', group: 'ai' },
  { key: 'classifiers', label: 'Classifiers', icon: 'filter', group: 'ai' },
  { key: 'search', label: 'Search', icon: 'search', group: 'ai' },
  { key: 'speech', label: 'Speech', icon: 'mic', group: 'ai' },
  { key: 'computer', label: 'Computer use', icon: 'monitor', group: 'ai' },
  { key: 'users', label: 'Users', icon: 'shield', group: 'server' },
  { key: 'ldap', label: 'LDAP', icon: 'link', group: 'server' },
  { key: 'about', label: 'About', icon: 'info', group: 'product' }
] as const
type Section = typeof SECTIONS[number]['key']

// Users and LDAP are for admins only (the server enforces it too). The web has no role from the shell: ask the server.
const isAdmin = ref(store.account.role === 'admin')
onMounted(async () => {
  try {
    if (store.transport) isAdmin.value = await store.transport.users().isAdmin()
  } catch {
    // Keep what the shell said.
  }
})
// What each section holds, so the filter finds "theme" under Appearance and "LDAP" under Users.
const KEYWORDS: Record<Section, string> = {
  usage: 'tokens cost spend money statistics chart',
  appearance: 'theme light dark mode system colours',
  sessions: 'permission modes ask auto plan bypass new sessions worktree defaults compact idle compaction context',
  sandbox: 'sandbox seatbelt restrict commands bash write paths network hosts proxy allowlist localhost',
  import: 'import bring claude code migrate memory skills agents commands output styles mcp sessions history',
  models: 'providers llm keys slots default model title effort',
  classifiers: 'classifier decision model rules',
  search: 'web search providers searxng brave tavily exa kagi perplexity',
  speech: 'voice dictation microphone read aloud stt tts parakeet whisper piper elevenlabs openai deepgram minimax',
  computer: 'computer use desktop apps control screen recording accessibility permissions click type',
  account: 'sign out server user',
  users: 'admin users roles passwords',
  ldap: 'ldap directory active directory sign in bind',
  about: 'version about'
}
const query = ref('')
const allowed = computed(() => SECTIONS.filter(s => s.group !== 'server' || isAdmin.value))
const sections = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? allowed.value.filter(s => `${s.label} ${KEYWORDS[s.key]}`.toLowerCase().includes(q)) : allowed.value
})

// Old links (?settings=general) open Appearance.
const section = computed<Section>({
  get: () => allowed.value.find(s => s.key === modal.section.value)?.key ?? 'appearance',
  set: v => modal.setSection(v)
})

// New-session defaults, shared with NewSession.vue.
const { prefs: defaults, save } = useSessionDefaults()
const prefs = computed(() => defaults.value)
const worktree = computed({ get: () => prefs.value.worktree ?? false, set: v => save('worktree', v) })
const sessionStore = useSessionStore()
const notifications = computed({
  get: () => sessionStore.deviceSettings.notifications,
  set: v => sessionStore.quietly(sessionStore.updateDeviceSettings({ notifications: v }))
})
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
      <template
        v-for="(s, i) in sections"
        :key="s.key"
      >
        <div
          v-if="i > 0 && sections[i - 1]!.group !== s.group"
          class="-mx-3 my-1.5 h-px shrink-0 bg-line max-md:hidden"
        />
        <button
          type="button"
          class="nav-row px-[9px] hover:bg-hover"
          :class="section === s.key ? 'nav-row-current' : ''"
          :aria-current="section === s.key ? 'page' : undefined"
          @click="section = s.key"
        >
          <HIcon :name="s.icon" />{{ s.label }}
        </button>
      </template>
      <p
        v-if="!sections.length"
        class="m-0 px-[9px] py-2 text-[12px] text-muted"
      >
        Nothing matches.
      </p>
    </nav>

    <div class="min-w-0 flex-1 overflow-y-auto px-(--set-gutter) pt-5 pb-8 [--set-gutter:28px] max-sm:[--set-gutter:16px]">
      <SettingsAppearance v-if="section === 'appearance'" />

      <SettingsUsage v-else-if="section === 'usage'" />

      <SettingsSandbox v-else-if="section === 'sandbox'" />

      <template v-else-if="section === 'sessions'">
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
        <div class="set-row">
          <div class="set-label">
            <span>Notifications</span>
            <span class="set-desc">Tell this device when a session finishes or needs you. Not synced.</span>
          </div>
          <HToggle
            v-model="notifications"
            label="Notifications"
          />
        </div>
        <SettingsCompaction />
        <SettingsLanguageServers />
        <SettingsPullRequests />
        <SettingsAgents />
      </template>

      <SettingsImport v-else-if="section === 'import'" />

      <template v-else-if="section === 'models' || section === 'classifiers'">
        <SettingsProviders
          :key="section"
          :group="section === 'models' ? 'llm' : 'classifier'"
        />
        <template v-if="section === 'models'">
          <SettingsLocalModels />
          <SettingsAcpAgents />
        </template>
      </template>

      <SettingsSearch v-else-if="section === 'search'" />
      <SettingsSpeech v-else-if="section === 'speech'" />
      <SettingsComputerUse v-else-if="section === 'computer'" />

      <SettingsUsers v-else-if="section === 'users'" />

      <SettingsLdap v-else-if="section === 'ldap'" />

      <SettingsAccount v-else-if="section === 'account'" />

      <SettingsAbout v-else />
    </div>
  </div>
</template>
