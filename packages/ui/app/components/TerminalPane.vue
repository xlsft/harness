<script setup lang="ts">
// Terminal pane: the session's terminals as tabs, each a real shell on this computer (TerminalView, the client's
// PTY). Terminals keep running when the pane closes and come back when it opens; closing a tab kills its shell.
// "+" and "Open in terminal" (useTerminalRequests) start new ones; an exited shell can be restarted in place.
import { usePanes } from '~/composables/usePanes'
import type { SessionState } from '~/lib/session/types'
import type { TerminalConnectionState } from '~/lib/terminal/connection'
import type { TerminalInfo } from '~/lib/terminal/types'
import { CURSOR_STYLES, type CursorStyle } from '~/lib/fixtures/terminal'

const props = defineProps<{ session: SessionState, menu: string }>()

const store = useSessionStore()
const toast = useToast()
const { closePane } = usePanes()
const { request } = useTerminalRequests()

interface Tab { info: TerminalInfo, ticket?: string, exitCode?: number, state?: TerminalConnectionState }
const tabs = ref<Tab[]>([])
const selected = ref('')
const tab = computed(() => tabs.value.find(t => t.info.id === selected.value))
const error = ref('')
const remote = computed(() => !!props.session.meta.remote)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function create(cwd?: string) {
  if (!store.transport || remote.value) return
  try {
    const { terminal, ticket } = await store.transport.terminals().create({ sessionId: props.session.meta.id, cwd })
    tabs.value.push({ info: terminal, ticket })
    selected.value = terminal.id
    error.value = ''
  } catch (e) {
    error.value = message(e)
  }
}

// The session's terminals that still exist (they outlive the pane); a new one when there are none.
async function load() {
  if (!store.transport || remote.value) return
  try {
    const list = await store.transport.terminals().list(props.session.meta.id)
    tabs.value = list.map(info => ({ info, exitCode: info.running ? undefined : info.exitCode }))
    selected.value = tabs.value.at(-1)?.info.id ?? ''
    if (!tabs.value.length && !request.value) await create()
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(load)
watch(() => props.session.meta.id, load)

watch(request, (r) => {
  if (r) {
    create(r.cwd)
    request.value = null
  }
}, { immediate: true })

async function closeTab(id: string) {
  const i = tabs.value.findIndex(t => t.info.id === id)
  if (i < 0) return
  tabs.value.splice(i, 1)
  if (selected.value === id) selected.value = tabs.value[Math.max(0, i - 1)]?.info.id ?? ''
  try {
    await store.transport?.terminals().kill(id)
  } catch { /* already gone */ }
  if (!tabs.value.length) closePane('terminal')
}

// An exited shell: a new one in the same folder takes its place.
async function restart() {
  const t = tab.value
  if (!t || !store.transport) return
  const id = t.info.id
  await create(t.info.cwd)
  const i = tabs.value.findIndex(x => x.info.id === id)
  if (i >= 0) tabs.value.splice(i, 1)
  store.transport.terminals().kill(id).catch(() => {})
}

async function killCurrent() {
  menuOpen.value = false
  if (tab.value) await closeTab(tab.value.info.id)
}

const views = ref<Record<string, { clear: () => void }>>({})
function clearCurrent() {
  menuOpen.value = false
  if (tab.value) views.value[tab.value.info.id]?.clear()
}

const menuOpen = ref(props.menu === 'settings')
const cursorMenu = ref(false)
watch(menuOpen, (v) => {
  if (!v) cursorMenu.value = false
})
const cursorStyle = ref<CursorStyle>('Block')
const blink = ref(true)
const xtermCursor = computed(() => cursorStyle.value.toLowerCase() as 'block' | 'bar' | 'underline')

function onExit(t: Tab, code: number) {
  t.exitCode = code
}
function title(t: Tab) {
  return t.info.title || t.info.cwd.split('/').pop() || 'shell'
}
async function copy(text: string) {
  await navigator.clipboard.writeText(text)
  toast.add({ title: 'Path copied', color: 'neutral' })
}
</script>

<template>
  <section
    aria-label="Terminal"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-stretch border-b border-line pr-1.5">
      <div class="flex items-center px-3 font-display text-[13px] text-strong">
        Terminal
      </div>
      <div
        role="tablist"
        aria-label="Terminal tabs"
        class="flex min-w-0 items-stretch overflow-hidden"
      >
        <PaneTab
          v-for="t in tabs"
          :key="t.info.id"
          :selected="t.info.id === selected"
          edge="left"
          class="gap-[7px] px-2.5"
          :title="t.info.cwd"
          @select="selected = t.info.id"
          @close="closeTab(t.info.id)"
        >
          <HIcon
            name="term"
            :size="13"
          />{{ title(t) }}<span
            v-if="t.exitCode !== undefined"
            class="text-[11px]"
            :class="t.exitCode ? 'text-bad' : 'text-muted'"
          >{{ t.exitCode }}</span><span
            v-else-if="t.state === 'reconnecting'"
            class="text-[11px] text-warn"
          >●</span>
        </PaneTab>
        <button
          v-if="!remote"
          type="button"
          aria-label="New terminal tab"
          title="New terminal tab"
          class="flex w-9 items-center justify-center border-x border-line text-muted hover:text-strong"
          @click="create()"
        >
          <HIcon name="plus" />
        </button>
      </div>
      <span class="flex-1" />
      <div class="flex items-center gap-[2px]">
        <PaneBtn
          icon="more"
          label="Terminal settings"
          white
          :pressed="menuOpen"
          data-menu-anchor
          @click="menuOpen = !menuOpen"
        />
        <PaneBtn
          icon="close"
          label="Close terminal pane"
          @click="closePane('terminal')"
        />
      </div>
    </div>

    <p
      v-if="remote"
      class="m-0 flex flex-1 items-center justify-center px-6 text-center text-[12.5px] text-muted"
    >
      This session runs on {{ session.meta.deviceName || 'another computer' }}; terminals open on the computer a session runs on.
    </p>
    <p
      v-else-if="error && !tabs.length"
      class="m-0 flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center text-[12.5px] text-muted"
    >
      <span class="text-bad">{{ error }}</span>
      <HBtn
        size="md"
        @click="create()"
      >
        Try again
      </HBtn>
    </p>
    <div
      v-else
      class="relative min-h-0 flex-1"
    >
      <TerminalView
        v-for="t in tabs"
        v-show="t.info.id === selected"
        :key="t.info.id"
        :ref="(el: any) => { if (el) views[t.info.id] = el; else delete views[t.info.id] }"
        :info="t.info"
        :ticket="t.ticket"
        :active="t.info.id === selected"
        :cursor-style="xtermCursor"
        :cursor-blink="blink"
        class="absolute inset-0"
        @exit="code => onExit(t, code)"
        @state="s => t.state = s"
      />
    </div>

    <div
      v-if="tab && tab.exitCode !== undefined"
      class="flex items-center gap-[9px] border-t border-l-2 border-t-line px-3 py-2 text-[12px] text-muted"
      :class="tab.exitCode ? 'border-l-bad' : 'border-l-line'"
    >
      Process exited with code <span :class="tab.exitCode ? 'text-bad' : 'text-strong'">{{ tab.exitCode }}</span><span class="flex-1" />
      <button
        type="button"
        class="border border-line px-[9px] py-[2px] text-[11px] text-strong hover:bg-hover"
        @click="restart"
      >
        Restart
      </button>
    </div>

    <PaneMenu v-model:open="menuOpen">
      <div
        v-if="cursorMenu"
        role="menu"
        aria-label="Cursor style"
        class="absolute top-[46px] right-[262px] z-30 menu-panel w-[170px]"
      >
        <PaneMenuItem
          v-for="s in CURSOR_STYLES"
          :key="s"
          role="menuitemradio"
          gap="gap-0"
          :checked="cursorStyle === s"
          :active="cursorStyle === s"
          @click="cursorStyle = s; menuOpen = false"
        >
          {{ s }}
        </PaneMenuItem>
      </div>
      <div
        role="menu"
        aria-label="Terminal settings"
        class="absolute top-[46px] right-10 z-30 menu-panel w-[220px]"
      >
        <PaneMenuItem
          :highlight="cursorMenu"
          :value="cursorStyle"
          sub
          @click="cursorMenu = !cursorMenu"
          @mouseenter="cursorMenu = true"
        >
          Cursor style
        </PaneMenuItem>
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="blink"
          @click="blink = !blink"
          @mouseenter="cursorMenu = false"
        >
          Blink cursor
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          :disabled="!tab"
          @click="clearCurrent"
          @mouseenter="cursorMenu = false"
        >
          Clear
        </PaneMenuItem>
        <PaneMenuItem
          :disabled="!tab"
          @click="tab && copy(tab.info.cwd); menuOpen = false"
          @mouseenter="cursorMenu = false"
        >
          Copy working directory
        </PaneMenuItem>
        <PaneMenuItem
          danger
          :disabled="!tab"
          @click="killCurrent"
          @mouseenter="cursorMenu = false"
        >
          Kill process
        </PaneMenuItem>
      </div>
    </PaneMenu>
  </section>
</template>
