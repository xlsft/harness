<script setup lang="ts">
// Settings page: sections on the left, the chosen one on the right (?section=). General edits the defaults the
// new-session screen starts from (the same localStorage key it remembers its last choices in).
import { isDesktop, logout as desktopLogout } from '~/lib/desktop'
import type { ProviderList } from '~/lib/providers/types'

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
const { prefs: defaults, save, effortOf, setEffort } = useSessionDefaults()
const prefs = computed(() => defaults.value)
const worktree = computed({ get: () => prefs.value.worktree ?? false, set: v => save('worktree', v) })
const serverDefault = computed(() => store.defaultModel || store.models[0]?.id || '')
const providerNames = ref<Record<string, string>>({})
function rememberProviders(list: ProviderList) {
  providerNames.value = Object.fromEntries(list.providers.map(p => [p.id, p.scope === 'personal' ? `${p.name} (yours)` : p.name]))
}
onMounted(async () => {
  try {
    if (store.transport) rememberProviders(await store.transport.providers().list())
  } catch {
    // Older clients do not forward ProviderService: group by id.
  }
})
// Models by provider, for the default-model picker.
const providers = computed(() => {
  const map = new Map<string, typeof store.models>()
  for (const m of store.models) {
    const p = m.provider ? providerNames.value[m.provider] ?? m.provider : 'Other'
    if (!map.has(p)) map.set(p, [])
    map.get(p)!.push(m)
  }
  return [...map.entries()].map(([name, models]) => ({ name, models }))
})
const defaultModel = computed(() => store.models.find(m => m.id === (prefs.value.model || serverDefault.value)))

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
    <div class="mx-auto flex max-w-[960px] gap-10 px-10 pt-10 pb-10 max-md:flex-col max-md:gap-5 max-sm:px-4 max-sm:pt-6">
      <nav
        aria-label="Settings sections"
        class="flex w-[180px] shrink-0 flex-col gap-[2px] max-md:w-full max-md:flex-row max-md:flex-wrap"
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

      <div class="flex min-w-0 flex-1 flex-col pt-[52px] max-md:pt-0">
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
              <span>Model</span>
              <span class="set-desc">Per-model effort is set under Models.</span>
            </div>
            <select
              :value="prefs.model ?? ''"
              aria-label="Default model"
              class="h-8 w-[300px] border border-accented bg-default px-2 text-[12.5px] text-white outline-none focus:border-accent max-sm:w-full"
              @change="save('model', ($event.target as HTMLSelectElement).value)"
            >
              <option value="">
                Server default{{ serverDefault ? ` (${serverDefault})` : '' }}
              </option>
              <optgroup
                v-for="p in providers"
                :key="p.name"
                :label="p.name"
              >
                <option
                  v-for="m in p.models"
                  :key="m.id"
                  :value="m.id"
                >
                  {{ m.id }}
                </option>
              </optgroup>
            </select>
          </div>
          <div
            v-if="defaultModel && modelEfforts(defaultModel).length"
            class="set-row"
          >
            <div class="set-label">
              <span>Effort</span>
              <span class="set-desc">How long {{ defaultModel.id }} thinks before answering.</span>
            </div>
            <div
              role="radiogroup"
              aria-label="Effort"
              class="flex gap-[2px]"
            >
              <button
                v-for="e in modelEfforts(defaultModel)"
                :key="e"
                type="button"
                role="radio"
                :aria-checked="effortOf(defaultModel) === e"
                class="border px-3 py-[5px] text-[12.5px]"
                :class="effortOf(defaultModel) === e ? 'border-line bg-raised text-white' : 'border-transparent text-toned hover:text-white'"
                @click="setEffort(defaultModel.id, e)"
              >
                {{ e }}
              </button>
            </div>
          </div>
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
          @changed="rememberProviders"
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

        <template v-else>
          <h2 class="set-h">
            About
          </h2>
          <p class="set-sub">
            Your workflow, unbounded.
          </p>
          <div class="set-row">
            <div class="set-label">
              <span>Client</span>
            </div>
            <span class="text-[12.5px] text-white">{{ store.clientVersion || '—' }}</span>
          </div>
          <div class="set-row">
            <div class="set-label">
              <span>App</span>
            </div>
            <span class="text-[12.5px] text-white">{{ desktop ? 'Desktop' : 'Web' }}</span>
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
