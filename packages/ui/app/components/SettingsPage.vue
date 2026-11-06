<script setup lang="ts">
import type { ManagedPolicyView } from '~/lib/transport/types'

// Settings (the body of SettingsModal): sections on the left with a filter, the chosen one on the right
// (?settings=<section>). General edits the defaults the new-session screen starts from (the same localStorage key
// it remembers its last choices in).

const props = defineProps<{
  // The web console without a client: only what talks to the server itself.
  serverOnly?: boolean
}>()

const store = useSessionStore()
const modal = useSettingsModal()
const access = useAccess()

// Grouped like the nav shows them: you, this app, the AI services, the server (admins), the product.
const SECTIONS = [
  { key: 'account', label: tt('Account'), icon: 'lock', group: 'you' },
  { key: 'usage', label: tt('Usage'), icon: 'chart', group: 'you' },
  { key: 'appearance', label: tt('Appearance'), icon: 'palette', group: 'app' },
  { key: 'sessions', label: tt('Sessions'), icon: 'session', group: 'app' },
  { key: 'sandbox', label: tt('Sandbox'), icon: 'box', group: 'app' },
  { key: 'hooks', label: tt('Hooks'), icon: 'hook', group: 'app' },
  { key: 'memory', label: tt('Memory'), icon: 'doc', group: 'app' },
  { key: 'codehosts', label: tt('Code hosts'), icon: 'pull-request', group: 'app' },
  { key: 'import', label: tt('Import'), icon: 'download', group: 'app' },
  { key: 'models', label: tt('Models'), icon: 'ai', group: 'ai' },
  { key: 'classifiers', label: tt('Classifiers'), icon: 'filter', group: 'ai' },
  { key: 'search', label: tt('Search'), icon: 'search', group: 'ai' },
  { key: 'speech', label: tt('Speech'), icon: 'mic', group: 'ai' },
  { key: 'computer', label: tt('Computer use'), icon: 'monitor', group: 'ai' },
  { key: 'ssh', label: tt('SSH hosts'), icon: 'terminal', group: 'ai' },
  { key: 'users', label: tt('Users'), icon: 'list', group: 'server' },
  { key: 'ldap', label: 'LDAP', icon: 'branch', group: 'server' },
  { key: 'sso', label: 'SSO', icon: 'globe', group: 'server' },
  { key: 'provisioning', label: tt('Provisioning'), icon: 'refresh', group: 'server' },
  { key: 'audit', label: tt('Audit log'), icon: 'history', group: 'server' },
  { key: 'policy', label: tt('Managed policy'), icon: 'flag', group: 'server' },
  { key: 'about', label: tt('About'), icon: 'info', group: 'product' }
] as const
type Section = typeof SECTIONS[number]['key']

// Server sections follow what the user's role allows (the server enforces it too).
const CAPABILITY: Partial<Record<Section, string>> = { users: 'users.read', ldap: 'sign_in.read', sso: 'sign_in.read', provisioning: 'sign_in.read', audit: 'audit.read', policy: 'policy.read' }
// Without the right to read it, the managed policy still shows when it imposes something on this user.
const policyApplies = ref(false)
const managed = ref<ManagedPolicyView | null>(null)
onMounted(async () => {
  await access.load()
  try {
    if (!props.serverOnly) {
      managed.value = (await store.transport?.getUserPreferences())?.managedPolicy ?? null
      policyApplies.value = managed.value?.active ?? false
    }
  } catch {
    // Then it does not show here; the client enforces it all the same.
  }
})
const SERVER_ONLY: Section[] = ['account', 'usage', 'models', 'users', 'ldap', 'sso', 'provisioning', 'audit', 'policy']
// What each section holds, so the filter finds "theme" under Appearance and "LDAP" under Users.
const KEYWORDS: Record<Section, string> = {
  usage: 'tokens cost spend money statistics chart',
  appearance: 'theme light dark mode system colours',
  sessions: 'permission modes ask auto plan bypass new sessions worktree defaults compact idle compaction context',
  sandbox: 'sandbox seatbelt restrict commands bash write paths network hosts proxy allowlist localhost',
  codehosts: 'github gitlab vcs code host pull request merge request pr connect app oauth gh glab webhook',
  hooks: 'hooks pretooluse posttooluse stop sessionstart scripts automation disable trust',
  memory: 'memory auto notes remember forget index project MEMORY.md',
  import: 'import bring claude code migrate memory skills agents commands output styles mcp sessions history',
  models: 'providers llm keys slots default model title effort',
  classifiers: 'classifier decision model rules',
  search: 'web search providers searxng brave tavily exa kagi perplexity',
  speech: 'voice dictation microphone read aloud stt tts parakeet whisper piper elevenlabs openai deepgram minimax',
  computer: 'computer use desktop apps control screen recording accessibility permissions click type your chrome real browser sites',
  ssh: 'ssh hosts remote machine server tunnel known hosts key',
  account: 'sign out server user',
  users: 'admin users roles passwords quotas limits spend budget groups auditor billing',
  ldap: 'ldap directory active directory sign in bind',
  sso: 'sso single sign-on oidc saml identity provider okta entra two-factor mfa totp require',
  provisioning: 'scim provisioning okta azure entra tokens groups',
  audit: 'audit log compliance events sign-ins csv export mcp tool calls servers',
  policy: 'managed policy permission rules sandbox required allowed providers mcp servers plugins marketplaces administrator',
  about: 'version about'
}
const query = ref('')
const allowed = computed(() => SECTIONS.filter((s) => {
  if (props.serverOnly && !SERVER_ONLY.includes(s.key)) return false
  if (s.group !== 'server') return true
  if (s.key === 'policy' && policyApplies.value) return true
  const cap = CAPABILITY[s.key]
  return !cap || access.can(cap)
}))
const sections = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? allowed.value.filter(s => `${s.label} ${KEYWORDS[s.key]}`.toLowerCase().includes(q)) : allowed.value
})

// Old links (?settings=general) open Appearance.
const section = computed<Section>({
  get: () => allowed.value.find(s => s.key === modal.section.value)?.key ?? (props.serverOnly ? 'account' : 'appearance'),
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
      :aria-label="tt('Settings sections')"
      class="flex w-[200px] shrink-0 flex-col gap-[2px] overflow-y-auto border-r border-line p-3 max-md:w-full max-md:flex-row max-md:flex-wrap max-md:border-r-0 max-md:border-b"
    >
      <label class="mb-2 flex h-9 items-center gap-2 border border-line px-2.5 text-muted focus-within:border-accent max-md:w-full">
        <HIcon
          name="search"
          :size="13"
        />
        <input
          v-model="query"
          :placeholder="tt('Search')"
          :aria-label="tt('Search settings')"
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
        {{ tt('Nothing matches.') }}
      </p>
    </nav>

    <div class="min-w-0 flex-1 overflow-y-auto px-(--set-gutter) pt-5 pb-8 [--set-gutter:28px] max-sm:[--set-gutter:16px]">
      <SettingsAppearance v-if="section === 'appearance'" />

      <SettingsUsage v-else-if="section === 'usage'" />

      <SettingsSandbox v-else-if="section === 'sandbox'" />

      <SettingsHooks v-else-if="section === 'hooks'" />

      <SettingsCodeHosts v-else-if="section === 'codehosts'" />

      <template v-else-if="section === 'sessions'">
        <template v-if="managed?.active">
          <h2 class="set-h">
            {{ tt('Set by your administrator') }}
          </h2>
          <p class="set-sub">
            {{ tt('This comes first. Nothing here or in a project\'s settings can change it.') }}
          </p>
          <SettingsManagedNotice
            :policy="managed"
            class="mb-6"
          />
        </template>
        <SettingsModes />
        <h2 class="set-h">
          {{ tt('New sessions') }}
        </h2>
        <p class="set-sub">
          {{ tt('What the new-session screen starts with. Changing a chip there updates these too.') }}
        </p>
        <div class="set-row">
          <div class="set-label">
            <span>{{ tt('Worktree') }}</span>
            <span class="set-desc">{{ tt('Run each new session in its own git worktree.') }}</span>
          </div>
          <HToggle
            v-model="worktree"
            :label="tt('Worktree by default')"
          />
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>{{ tt('Notifications') }}</span>
            <span class="set-desc">{{ tt('Tell this device when a session finishes or needs you. Not synced.') }}</span>
          </div>
          <HToggle
            v-model="notifications"
            :label="tt('Notifications')"
          />
        </div>
        <SettingsCompaction />
        <SettingsLanguageServers />
        <SettingsPullRequests />
        <SettingsAgents />
      </template>

      <SettingsMemory v-else-if="section === 'memory'" />

      <SettingsImport v-else-if="section === 'import'" />


      <template v-else-if="section === 'models' || section === 'classifiers'">
        <p
          v-if="section === 'models' && (managed?.restrictProviders || managed?.personalProvidersDisabled)"
          role="status"
          class="m-0 mb-4 border border-default px-3 py-2 text-[12px] text-toned"
        >
          {{ tt('Set by your administrator:') }}
          <template v-if="managed?.restrictProviders">
            {{ tt('only {0} may be used.', [managed.allowedProviders.join(', ') || 'no providers']) }}
          </template>
          <template v-if="managed?.personalProvidersDisabled">
            {{ tt('Personal providers are not allowed.') }}
          </template>
        </p>
        <SettingsProviders
          :key="section"
          :group="section === 'models' ? 'llm' : 'classifier'"
        />
        <template v-if="section === 'models' && !serverOnly">
          <SettingsLocalModels />
          <SettingsAcpAgents />
        </template>
      </template>

      <SettingsSearch v-else-if="section === 'search'" />
      <SettingsSpeech v-else-if="section === 'speech'" />
      <SettingsComputerUse v-else-if="section === 'computer'" />
      <SettingsSshHosts v-else-if="section === 'ssh'" />

      <SettingsUsers v-else-if="section === 'users'" />

      <SettingsLdap v-else-if="section === 'ldap'" />

      <SettingsSso v-else-if="section === 'sso'" />

      <SettingsProvisioning v-else-if="section === 'provisioning'" />

      <SettingsAudit v-else-if="section === 'audit'" />

      <SettingsManagedPolicy v-else-if="section === 'policy'" />

      <SettingsAccount v-else-if="section === 'account'" />

      <SettingsAbout v-else />
    </div>
  </div>
</template>
