<script setup lang="ts">
// Sessions sidebar (Main.dc.html): window row, wordmark, primary nav, sessions grouped by project (sessions
// without a folder under Chats, first), account footer.
// Sub-sessions (forks) are listed under their parent with an └ connector; a parent row folds them (▾/▸).
import { avatarUrl } from '~/lib/account/types'
import { isDesktop } from '~/lib/desktop'
import type { SessionState } from '~/lib/session/types'
import { groupSessions, limitTree, sessionTree } from '~/lib/session/tree'

const store = useSessionStore()
const route = useRoute()
const collapsed = useState('sidebar-collapsed', () => false)
const open = useState('sidebar-open', () => false)
const expanded = ref<Record<string, boolean>>({})

const VISIBLE = 5

// Parents whose sub-sessions are folded (the ▸ on their row).
const folded = useState<Record<string, boolean>>('sidebar-folded', () => ({}))
function toggleFold(id: string) {
  folded.value = { ...folded.value, [id]: !folded.value[id] }
}

const pinned = computed(() => store.sessionList.filter(s => s.meta.pinned))
const pinnedRows = computed(() => sessionTree(pinned.value, folded.value))

const groups = computed(() => groupSessions(store.sessionList.filter(s => !s.meta.pinned)).map(({ key, name, chats, sessions }) => {
  // Folder for "+": the latest local session's, or its main checkout when it ran in a worktree (sessions of
  // other machines name folders over there; a group of only those has no "+"). Chats' "+" starts without one.
  const local = chats ? undefined : sessions.find(s => !s.meta.remote)
  // Sub-sessions follow their parent; the limit counts top-level rows.
  const tree = sessionTree(sessions, folded.value)
  const shown = expanded.value[key] ? { rows: tree, more: false } : limitTree(tree, VISIBLE)
  return {
    key,
    name,
    chats,
    cwd: local ? local.meta.worktree?.repositoryPath || local.meta.cwd : '',
    rows: shown.rows,
    more: shown.more
  }
}))

// Sections in order: Pinned (when any), then Chats (sessions without a folder) and the folder groups.
const sections = computed(() => [
  ...(pinned.value.length ? [{ key: '\0pinned', name: 'Pinned', chats: false, cwd: '', rows: pinnedRows.value, more: false, pin: true }] : []),
  ...groups.value
])

// Indent of a row's title: one step per level under the top-level row.
// A sub-session's title starts past its parent's (whose fold chevron pushes it 22px right); the └ connector
// starts under the parent's chevron.
const STEP = 26
const indent = (depth: number) => ({ paddingLeft: `${9 + depth * STEP}px` })
const connector = (depth: number) => ({ left: `${12 + (depth - 1) * STEP}px`, width: `${STEP - 8}px` })

// Tooltip of a remote session's device badge.
function deviceTitle(s: SessionState) {
  const where = `Runs on ${s.meta.deviceName || 'another machine'}`
  if (s.meta.remoteControllable) return `${where} · online, remote control available`
  if (s.meta.deviceOnline === true) return `${where} · online, not open there`
  if (s.meta.deviceOnline === false) return `${where} · offline`
  return where
}

const onArtifacts = computed(() => route.path.startsWith('/artifacts'))
const settings = useSettingsModal()
const searchOpen = useState('search-open', () => false)
const onSettings = settings.isOpen
// A page other than the session view is open (or the new-session screen): no session row is current.
const onExtensions = computed(() => route.path.startsWith('/extensions'))
const onPage = computed(() => onArtifacts.value || onExtensions.value || !!route.query.new)

function mark(s: SessionState) {
  if (s.meta.status === 'error') return { text: 'fail', cls: 'text-bad' }
  if (s.meta.status === 'working' || s.meta.status === 'needs_input') return { dot: true }
  if (s.meta.pullRequest) return { text: `PR ${s.meta.pullRequest.number}`, cls: 'text-muted' }
  if (s.meta.additions) return { text: `+${s.meta.additions}`, cls: 'text-ok' }
  return null
}

// The session whose menu is open (the "…" or a right click) and the one being renamed in place.
const menuFor = ref('')
const renaming = ref('')
const draft = ref('')
function startRename(s: SessionState) {
  draft.value = s.meta.title
  renaming.value = s.meta.id
  nextTick(() => (document.querySelector('[data-rename]') as HTMLInputElement | null)?.select())
}
function finishRename(save: boolean) {
  if (save && renaming.value) store.quietly(store.rename(renaming.value, draft.value))
  renaming.value = ''
}

function openSession(id: string) {
  store.open(id)
  open.value = false
  if (route.path !== '/' || route.query.new) navigateTo({ path: '/' })
}

// The new-session screen; `cwd` preselects a folder (the "+" next to a group).
function newSession(cwd?: string) {
  open.value = false
  navigateTo({ path: '/', query: { new: '1', ...(cwd ? { cwd } : {}) } })
}

const desktop = isDesktop()
const update = useAppUpdate()

// Footer: who is signed in and on which server.
// The profile (name, avatar) from the server; the shell's whoami until it loads.
const { profile, load: loadProfile } = useAccountProfile()
onMounted(() => loadProfile())
const displayName = computed(() => profile.value?.name || store.account.name || store.userEmail.split('@')[0] || 'Not signed in')
const host = computed(() => {
  try {
    return store.account.server ? new URL(store.account.server).host : ''
  } catch {
    return store.account.server
  }
})

const NuxtLink = resolveComponent('NuxtLink')

// `soon`: not built yet; the row shows a badge (Artifacts keeps its preview page, the others do nothing).
const nav = [
  { label: 'Artifacts', icon: 'copy' as const, to: '/artifacts', soon: true },
  { label: 'Scheduled', icon: 'clock' as const, soon: true },
  { label: 'Extensions', icon: 'sliders' as const, to: '/extensions' },
  { label: 'Knowledge base', icon: 'page' as const, soon: true },
  { label: 'Task tracker', icon: 'tasks' as const, soon: true }
]
</script>

<template>
  <UDashboardSidebar
    id="sessions"
    v-model:collapsed="collapsed"
    v-model:open="open"
    collapsible
    resizable
    :collapsed-size="0"
    :default-size="240"
    :min-size="0"
    :max-size="440"
    :toggle="false"
    :ui="{
      // The desktop app keeps the column at any window width (half a screen is still a desktop); a narrow browser
      // tab gets the slide-over menu instead, without Nuxt UI's empty header row (ours is the top row below).
      root: ['bg-default border-e border-(--ui-divider) min-w-0 data-[collapsed=true]:hidden', desktop && 'flex'],
      header: 'hidden',
      body: 'p-0 gap-0 overflow-hidden sm:px-0',
      footer: 'h-[46px] gap-[9px] border-t border-default py-0 pr-2.5 pl-3.5 sm:px-3.5',
      content: ['bg-default rounded-none max-w-[240px]', desktop && 'hidden'],
      overlay: desktop ? 'hidden' : '',
      // The border is the divider; the handle only widens the grab area and lights up while used.
      handle: 'relative z-10 -ml-px w-px bg-transparent transition-colors hover:bg-primary data-[dragging=true]:bg-primary'
    }"
    aria-label="Sessions"
  >
    <!-- Top row: drags the window in the desktop app, and leaves room for the macOS window buttons there. -->
    <div
      data-tauri-drag-region
      class="flex h-11 shrink-0 items-center gap-[7px] border-b border-default pr-3"
      :class="desktop ? 'pl-[92px]' : 'pl-3'"
    >
      <span
        data-tauri-drag-region
        class="flex-1 self-stretch"
      />
      <HIconBtn
        icon="sidebar"
        label="Toggle sidebar"
        kbd="meta+b"
        @click="collapsed = !collapsed; open = false"
      />
    </div>

    <div class="flex flex-col border-b border-default px-[5px] py-1.5">
      <a
        href="#"
        class="nav-row px-[9px] hover:bg-hover text-strong"
        @click.prevent="newSession()"
      >
        <HIcon name="plus" />New session<span class="flex-1" /><HKeys keys="meta+n" />
      </a>
      <a
        href="#"
        class="nav-row px-[9px] hover:bg-hover text-strong"
        @click.prevent="searchOpen = true; open = false"
      >
        <HIcon name="search" />Search<span class="flex-1" /><HKeys keys="meta+k" />
      </a>
      <component
        :is="item.to ? NuxtLink : 'a'"
        v-for="item in nav"
        :key="item.label"
        :to="item.to"
        :href="item.to ? undefined : '#'"
        class="nav-row px-[9px] hover:bg-hover"
        :aria-disabled="item.soon && !item.to ? true : undefined"
        :class="(item.to === '/artifacts' && onArtifacts) || (item.to === '/extensions' && onExtensions) ? 'nav-row-current' : ''"
        :aria-current="(item.to === '/artifacts' && onArtifacts) || (item.to === '/extensions' && onExtensions) ? 'page' : undefined"
        @click="item.soon && !item.to && $event.preventDefault()"
      >
        <HIcon :name="item.icon" />{{ item.label }}<span
          v-if="item.soon"
          class="ml-auto border border-default px-1 text-[10px] leading-[14px] text-muted"
        >soon</span>
      </component>
    </div>

    <div class="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-[5px] py-[11px]">
      <section
        v-for="g in sections"
        :key="g.key"
        class="flex flex-col"
      >
        <div class="group/head flex items-center gap-1 px-[9px] pb-1 text-muted">
          <HIcon
            v-if="'pin' in g"
            name="pin"
            :size="11"
            class="shrink-0"
          />
          <span class="truncate text-[12px]">{{ g.name }}</span>
          <HIcon
            v-if="!('pin' in g)"
            name="down"
            :size="11"
            class="shrink-0"
          />
          <span class="flex-1" />
          <button
            v-if="g.cwd || g.chats"
            type="button"
            :aria-label="g.chats ? 'New session without a folder' : `New session in ${g.name}`"
            :title="g.chats ? 'New session without a folder' : `New session in ${g.cwd}`"
            class="flex size-5 shrink-0 items-center justify-center text-muted hover:bg-hover hover:text-strong"
            @click="newSession(g.cwd || undefined)"
          >
            <HIcon
              name="plus"
              :size="13"
            />
          </button>
        </div>
        <template
          v-for="{ session: s, depth, children, collapsed: isFolded } in g.rows"
          :key="s.meta.id"
        >
          <input
            v-if="renaming === s.meta.id"
            v-model="draft"
            data-rename
            aria-label="Session title"
            class="mx-[2px] h-7 border border-accent bg-default px-[7px] text-[12.5px] text-strong outline-none"
            :style="depth ? { marginLeft: `${2 + depth * STEP}px` } : undefined"
            @keydown.enter.prevent="finishRename(true)"
            @keydown.esc.prevent="finishRename(false)"
            @blur="finishRename(true)"
          >
          <a
            v-else
            href="#"
            class="group/row nav-row relative pr-[9px] [contain-intrinsic-size:auto_28px] [content-visibility:auto] hover:bg-hover"
            :class="[!onPage && s.meta.id === store.currentId ? 'nav-row-current' : '', menuFor === s.meta.id ? 'bg-hover' : '']"
            :style="indent(depth)"
            :data-depth="depth"
            @click.prevent="openSession(s.meta.id)"
            @contextmenu.prevent="menuFor = s.meta.id"
            @dblclick.prevent="startRename(s)"
          >
            <!-- └ connector of a sub-session -->
            <span
              v-if="depth"
              aria-hidden="true"
              class="pointer-events-none absolute top-0 h-1/2 rounded-none border-b border-l border-line-strong"
              :style="connector(depth)"
            />
            <button
              v-if="children"
              type="button"
              :aria-label="isFolded ? `Show ${children} sub-sessions` : `Hide ${children} sub-sessions`"
              :aria-expanded="!isFolded"
              class="-ml-[3px] flex size-4 shrink-0 items-center justify-center text-muted hover:text-strong"
              @click.stop.prevent="toggleFold(s.meta.id)"
            >
              <HIcon
                :name="isFolded ? 'right' : 'down'"
                :size="11"
              />
            </button>
            <span
              class="truncate"
              :class="s.meta.unread && 'font-semibold text-strong'"
            >{{ s.meta.title }}</span>
            <span class="flex-1" />
            <span
              v-if="s.meta.remote && menuFor !== s.meta.id"
              :title="deviceTitle(s)"
              class="max-w-[84px] shrink-0 truncate text-[10.5px] text-muted group-hover/row:hidden"
              :class="s.meta.deviceOnline === false && 'opacity-60'"
            >{{ s.meta.deviceName || 'remote' }}</span>
            <SessionMenu
              :session="s"
              :sub="depth > 0"
              :children="menuFor === s.meta.id ? store.childrenOf(s.meta.id).length : 0"
              :open="menuFor === s.meta.id"
              @update:open="menuFor = $event ? s.meta.id : ''"
              @rename="startRename(s)"
            />
            <template v-if="menuFor !== s.meta.id">
              <span
                v-if="s.meta.unread"
                class="size-1.5 shrink-0 rounded-full bg-accent group-hover/row:hidden"
              />
              <template v-else-if="mark(s)">
                <span
                  v-if="mark(s)!.dot"
                  class="size-1.5 shrink-0 bg-accent group-hover/row:hidden"
                />
                <span
                  v-else
                  class="text-[10.5px] group-hover/row:hidden"
                  :class="mark(s)!.cls"
                >{{ mark(s)!.text }}</span>
              </template>
            </template>
          </a>
        </template>
        <a
          v-if="g.more"
          href="#"
          class="nav-row px-[9px] hover:bg-hover text-[11px] text-muted"
          @click.prevent="expanded[g.key] = true"
        >Show more</a>
      </section>
    </div>
    <button
      v-if="update.phase.value !== 'idle'"
      type="button"
      class="flex h-8 shrink-0 items-center gap-2 border-t border-default px-3.5 text-left text-[11.5px] text-muted hover:bg-hover hover:text-strong"
      @click="settings.open('about'); open = false"
    >
      <span class="size-1.5 shrink-0 bg-accent" />
      <span class="truncate">{{ update.phase.value === 'installed' ? 'Restart to update' : 'Update available' }} · v{{ update.version.value }}</span>
    </button>

    <template #footer>
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
      <span
        class="flex min-w-0 items-baseline gap-1.5"
        :title="store.userEmail"
      >
        <!-- The host gives way first, then the name. -->
        <span class="min-w-[4ch] truncate text-[12px] text-strong">{{ displayName }}</span>
        <span
          v-if="host"
          class="min-w-0 shrink-[9999] truncate text-[10.5px] text-muted"
        >{{ host }}</span>
      </span>
      <span class="flex-1" />
      <HIconBtn
        icon="settings"
        label="Settings"
        kbd="meta+,"
        :pressed="onSettings"
        @click="settings.open(); open = false"
      />
    </template>
  </UDashboardSidebar>
</template>
