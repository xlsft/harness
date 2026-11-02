<script setup lang="ts">
// Settings page: sections on the left, the chosen one on the right (?section=). General edits the defaults the
// new-session screen starts from (the same localStorage key it remembers its last choices in).
import type { EffortName, PermissionModeName } from '~/lib/session/types'
import { isDesktop, logout as desktopLogout } from '~/lib/desktop'

const store = useSessionStore()
const route = useRoute()
const router = useRouter()
const desktop = isDesktop()

const SECTIONS = [
  { key: 'general', label: 'General', icon: 'sliders' },
  { key: 'models', label: 'Models', icon: 'ai' },
  { key: 'account', label: 'Account', icon: 'lock' },
  { key: 'about', label: 'About', icon: 'info' }
] as const
type Section = typeof SECTIONS[number]['key']

const section = computed<Section>({
  get: () => SECTIONS.find(s => s.key === route.query.section)?.key ?? 'general',
  set: v => router.replace({ query: { ...route.query, section: v === 'general' ? undefined : v } })
})

// New-session defaults, shared with NewSession.vue.
const PREFS = 'harness-new-session'
interface Prefs { cwd?: string, worktree?: boolean, model?: string, effort?: EffortName, mode?: PermissionModeName }
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
          v-for="s in SECTIONS"
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
              <span class="set-desc">The server's catalog; the first one is used when none is chosen.</span>
            </div>
            <select
              :value="prefs.model ?? ''"
              aria-label="Default model"
              class="h-8 w-[300px] border border-accented bg-default px-2 text-[12.5px] text-white outline-none focus:border-accent max-sm:w-full"
              @change="save('model', ($event.target as HTMLSelectElement).value)"
            >
              <option value="">
                Server default{{ store.models[0] ? ` (${store.models[0].id})` : '' }}
              </option>
              <option
                v-for="m in store.models"
                :key="m.id"
                :value="m.id"
              >
                {{ m.id }}
              </option>
            </select>
          </div>
          <div class="set-row">
            <div class="set-label">
              <span>Effort</span>
              <span class="set-desc">How long the model thinks before answering.</span>
            </div>
            <div
              role="radiogroup"
              aria-label="Effort"
              class="flex gap-[2px]"
            >
              <button
                v-for="e in EFFORTS"
                :key="e"
                type="button"
                role="radio"
                :aria-checked="(prefs.effort ?? 'medium') === e"
                class="border px-3 py-[5px] text-[12.5px] capitalize"
                :class="(prefs.effort ?? 'medium') === e ? 'border-line bg-raised text-white' : 'border-transparent text-toned hover:text-white'"
                @click="save('effort', e)"
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
            The models your server offers. Providers and keys are managed on the server.
          </p>
          <div class="border border-default">
            <div
              v-for="m in store.models"
              :key="m.id"
              class="flex items-center gap-3 border-b border-default px-3 py-2.5 text-[12.5px] last:border-b-0"
            >
              <span class="min-w-0 flex-1 truncate text-white">{{ m.id }}</span>
              <span class="text-[11.5px] text-muted">{{ m.provider }}</span>
              <span
                v-if="m.contextWindow"
                class="w-14 text-right text-[11.5px] text-muted"
              >{{ Math.round(m.contextWindow / 1000) }}k</span>
              <span class="w-16 text-right text-[11.5px] text-muted">{{ m.price || '—' }}</span>
            </div>
            <p
              v-if="!store.models.length"
              class="m-0 px-3 py-6 text-center text-[12.5px] text-muted"
            >
              No models yet.
            </p>
          </div>
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
