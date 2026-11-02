<script setup lang="ts">
// Sessions sidebar (Main.dc.html): window row, wordmark, primary nav, sessions grouped by project, account footer.
import type { SessionState } from '~/lib/session/types'

const store = useSessionStore()
const route = useRoute()
const collapsed = useState('sidebar-collapsed', () => false)
const open = useState('sidebar-open', () => false)
const expanded = ref<Record<string, boolean>>({})

const VISIBLE = 5

const groups = computed(() => {
  const map = new Map<string, SessionState[]>()
  for (const s of store.sessionList) {
    const name = basename(s.meta.repo || s.meta.cwd) || 'other'
    if (!map.has(name)) map.set(name, [])
    map.get(name)!.push(s)
  }
  return [...map.entries()].map(([name, sessions]) => ({
    name,
    sessions: expanded.value[name] ? sessions : sessions.slice(0, VISIBLE),
    more: sessions.length > VISIBLE && !expanded.value[name]
  }))
})

const onArtifacts = computed(() => route.path.startsWith('/artifacts'))

function mark(s: SessionState) {
  if (s.meta.status === 'error') return { text: 'fail', cls: 'text-bad' }
  if (s.meta.status === 'working' || s.meta.status === 'needs_input') return { dot: true }
  if (s.meta.pullRequest) return { text: `PR ${s.meta.pullRequest.number}`, cls: 'text-muted' }
  if (s.meta.additions) return { text: `+${s.meta.additions}`, cls: 'text-ok' }
  return null
}

function openSession(id: string) {
  store.open(id)
  open.value = false
  if (route.path !== '/' || route.query.new) navigateTo({ path: '/', query: route.query.panes ? { panes: route.query.panes } : {} })
}

function newSession() {
  open.value = false
  navigateTo({ path: '/', query: { ...(route.query.panes ? { panes: route.query.panes } : {}), new: '1' } })
}

const nav = [
  { label: 'Artifacts', icon: 'copy' as const, to: '/artifacts' },
  { label: 'Scheduled', icon: 'clock' as const },
  { label: 'Customize', icon: 'sliders' as const }
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
    :default-size="264"
    :min-size="0"
    :max-size="440"
    :toggle="false"
    :ui="{
      root: 'bg-surface border-e border-(--ui-divider) min-w-0 data-[collapsed=true]:hidden',
      body: 'p-0 gap-0 overflow-hidden',
      footer: 'h-[46px] gap-[9px] border-t border-default py-0 pr-2.5 pl-3.5 sm:px-3.5',
      content: 'bg-surface rounded-none max-w-[264px]',
      // The border is the divider; the handle only widens the grab area and lights up while used.
      handle: 'relative z-10 -ml-px w-px bg-transparent transition-colors hover:bg-primary data-[dragging=true]:bg-primary'
    }"
    aria-label="Sessions"
  >
    <div class="flex h-11 shrink-0 items-center gap-[7px] border-b border-default px-3">
      <span class="size-[11px] bg-line" />
      <span class="size-[11px] bg-line" />
      <span class="size-[11px] bg-line" />
      <span class="flex-1" />
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
        class="nav-row px-[9px] hover:bg-hover text-white"
        @click.prevent="newSession"
      >
        <HIcon name="plus" />New session<span class="flex-1" /><HKeys keys="meta+n" />
      </a>
      <component
        :is="item.to ? 'NuxtLink' : 'a'"
        v-for="item in nav"
        :key="item.label"
        :to="item.to"
        :href="item.to ? undefined : '#'"
        class="nav-row px-[9px] hover:bg-hover"
        :class="item.to === '/artifacts' && onArtifacts ? 'nav-row-current' : ''"
        :aria-current="item.to === '/artifacts' && onArtifacts ? 'page' : undefined"
      >
        <HIcon :name="item.icon" />{{ item.label }}
      </component>
    </div>

    <div class="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-[5px] py-[11px]">
      <section
        v-for="g in groups"
        :key="g.name"
        class="flex flex-col"
      >
        <div class="flex items-center gap-1 px-[9px] pb-1 text-muted">
          <span class="text-[12px]">{{ g.name }}</span>
          <HIcon
            name="down"
            :size="11"
          />
        </div>
        <a
          v-for="s in g.sessions"
          :key="s.meta.id"
          href="#"
          class="nav-row px-[9px] hover:bg-hover"
          :class="!onArtifacts && s.meta.id === store.currentId ? 'nav-row-current' : ''"
          @click.prevent="openSession(s.meta.id)"
        >
          <span class="truncate">{{ s.meta.title }}</span>
          <span class="flex-1" />
          <template v-if="mark(s)">
            <span
              v-if="mark(s)!.dot"
              class="size-1.5 shrink-0 bg-accent"
            />
            <span
              v-else
              class="text-[10.5px]"
              :class="mark(s)!.cls"
            >{{ mark(s)!.text }}</span>
          </template>
        </a>
        <a
          v-if="g.more"
          href="#"
          class="nav-row px-[9px] hover:bg-hover text-[11px] text-muted"
          @click.prevent="expanded[g.name] = true"
        >Show more</a>
      </section>
    </div>

    <template #footer>
      <span class="flex size-[22px] items-center justify-center bg-accent text-[10.5px] font-bold text-white">X</span>
      <span class="text-[12px] whitespace-nowrap text-white">xlsft</span><span class="text-[10.5px] whitespace-nowrap text-muted">· self-hosted</span>
      <span class="flex-1" />
      <HIconBtn
        icon="settings"
        label="Settings"
      />
      <HIconBtn
        icon="search"
        label="Search"
        kbd="meta+k"
      />
    </template>
  </UDashboardSidebar>
</template>
