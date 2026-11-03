<script setup lang="ts">
// Settings page: sections on the left, the chosen one on the right (?section=). General edits the defaults the
// new-session screen starts from (the same localStorage key it remembers its last choices in).
import type { EffortName, PermissionModeName } from '~/lib/session/types'
import { isDesktop, logout as desktopLogout } from '~/lib/desktop'
import type { ProviderList } from '~/lib/providers/types'

const store = useSessionStore()
const route = useRoute()
const router = useRouter()
const desktop = isDesktop()

const SECTIONS = [
  { key: 'general', label: 'General', icon: 'sliders' },
  { key: 'models', label: 'Models', icon: 'ai' },
  { key: 'providers', label: 'Providers', icon: 'box' },
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
const PREFS = 'harness-new-session'
interface Prefs {
  cwd?: string
  worktree?: boolean
  model?: string
  effort?: EffortName
  mode?: PermissionModeName
  // Default effort per model; levels differ between models.
  effortByModel?: Record<string, EffortName>
}
const prefs = reactive<Prefs>({})
onMounted(() => {
  try {
    Object.assign(prefs, JSON.parse(localStorage.getItem(PREFS) ?? '{}') as Prefs)
  } catch {
    // Defaults.
  }
})
function save<K extends keyof Prefs>(key: K, value: Prefs[K]) {
  prefs[key] = value
  try {
    localStorage.setItem(PREFS, JSON.stringify(prefs))
  } catch {
    // Not remembered.
  }
}
const worktree = computed({ get: () => prefs.worktree ?? false, set: v => save('worktree', v) })

const serverDefault = computed(() => store.defaultModel || store.models[0]?.id || '')
// Provider names for grouping (personal provider ids are generated); ids until the list arrives.
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
const providers = computed(() => {
  const map = new Map<string, typeof store.models>()
  for (const m of store.models) {
    const p = m.provider ? providerNames.value[m.provider] ?? m.provider : 'Other'
    if (!map.has(p)) map.set(p, [])
    map.get(p)!.push(m)
  }
  return [...map.entries()].map(([name, models]) => ({ name, models }))
})
const defaultModel = computed(() => store.models.find(m => m.id === (prefs.model || serverDefault.value)))

function effortOf(m: { id: string, efforts?: EffortName[] }) {
  return fitEffort(prefs.effortByModel?.[m.id] ?? prefs.effort ?? 'medium', modelEfforts(m))
}
function setEffort(id: string, e: EffortName) {
  save('effortByModel', { ...prefs.effortByModel, [id]: e })
}
function ctx(n: number) {
  return n >= 1_000_000 ? `${n / 1_000_000}M` : `${Math.round(n / 1000)}k`
}

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
          <h2 class="set-h">
            New sessions
          </h2>
          <p class="set-sub">
            What the new-session screen starts with. Changing a chip there updates these too.
          </p>
          <div class="set-row">
            <div class="set-label">
              <span>Permission mode</span>
              <span class="set-desc">How much the agent may do without asking.</span>
            </div>
            <div class="flex flex-col border border-default">
              <button
                v-for="m in MODES"
                :key="m.key"
                type="button"
                role="radio"
                :aria-checked="(prefs.mode ?? 'auto') === m.key"
                class="flex w-[300px] items-center gap-2.5 px-3 py-2 text-left hover:bg-hover max-sm:w-full"
                :class="(prefs.mode ?? 'auto') === m.key ? 'bg-raised' : ''"
                @click="save('mode', m.key)"
              >
                <span class="flex-1">
                  <span
                    class="block text-[12.5px]"
                    :class="m.cls"
                  >{{ m.label }}</span>
                  <span class="block text-[11.5px] text-muted">{{ m.detail }}</span>
                </span>
                <HIcon
                  v-if="(prefs.mode ?? 'auto') === m.key"
                  name="check"
                  :size="14"
                  class="text-accent"
                />
              </button>
            </div>
          </div>
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

        <template v-else-if="section === 'models'">
          <h2 class="set-h">
            Models
          </h2>
          <p class="set-sub">
            What you can use, by provider: the server's models and your own. Each model keeps its own effort; add providers and keys under Providers.
          </p>
          <div class="flex flex-col gap-6">
            <section
              v-for="p in providers"
              :key="p.name"
              :aria-label="p.name"
              class="flex flex-col"
            >
              <div class="flex items-center gap-2 pb-2 text-[12px] text-muted">
                <span class="text-white">{{ p.name }}</span>
                <span>{{ p.models.length }} {{ p.models.length === 1 ? 'model' : 'models' }}</span>
              </div>
              <div class="border border-default">
                <div
                  v-for="m in p.models"
                  :key="m.id"
                  class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-default px-3 py-2.5 text-[12.5px] last:border-b-0"
                >
                  <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span class="flex items-center gap-2 truncate text-white">
                      {{ m.id }}
                      <span
                        v-if="m.id === serverDefault"
                        class="border border-default px-1 text-[10.5px] text-muted"
                      >default</span>
                    </span>
                    <span class="text-[11.5px] text-muted">
                      {{ [m.contextWindow ? `${ctx(m.contextWindow)} context` : '', m.price ? `${m.price} per 1M` : '', m.thinking ? 'thinking' : ''].filter(Boolean).join(' · ') || '—' }}
                    </span>
                  </span>
                  <div
                    v-if="modelEfforts(m).length"
                    role="radiogroup"
                    :aria-label="`Effort for ${m.id}`"
                    class="flex gap-[2px]"
                  >
                    <button
                      v-for="e in modelEfforts(m)"
                      :key="e"
                      type="button"
                      role="radio"
                      :aria-checked="effortOf(m) === e"
                      class="border px-2 py-[3px] text-[11.5px]"
                      :class="effortOf(m) === e ? 'border-line bg-raised text-white' : 'border-transparent text-muted hover:text-white'"
                      @click="setEffort(m.id, e)"
                    >
                      {{ e }}
                    </button>
                  </div>
                  <span
                    v-else
                    class="text-[11.5px] text-dimmed"
                  >no effort levels</span>
                </div>
              </div>
            </section>
            <p
              v-if="!store.models.length"
              class="m-0 border border-default px-3 py-6 text-center text-[12.5px] text-muted"
            >
              No models yet.
            </p>
          </div>
        </template>

        <SettingsProviders
          v-else-if="section === 'providers'"
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
