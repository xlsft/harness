<script setup lang="ts">
// Terminal pane (docs/design/ui/Terminal.dc.html). Tabs and scrollback are demo fixtures; the
// variants of the artboard are tabs here: "harness" is the shell session, "core dev" was started
// by the agent (banner), "go test" exited non-zero (footer). `menu=settings` opens the settings
// menu with the cursor-style submenu, `menu=agent` / `menu=exited` select those tabs.
import { usePanes } from '~/composables/usePanes'
import type { SessionState } from '~/lib/session/types'
import { CURSOR_STYLES, promptLines, terminalTabs, type CursorStyle, type TermTab } from '~/lib/fixtures/terminal'

const props = defineProps<{ session: SessionState, menu: string }>()

const { closePane } = usePanes()
const now = useNow()

const tabs = ref<TermTab[]>(terminalTabs())
const selected = ref(props.menu === 'agent' ? 'core-dev' : props.menu === 'exited' ? 'go-test' : 'harness')
const tab = computed(() => tabs.value.find(t => t.id === selected.value) ?? tabs.value[0])

const loadedAt = Date.now()
const agentElapsed = computed(() => tab.value?.agent ? formatDuration(now.value - loadedAt + tab.value.agent.startedAgoMs) : '')

const menuOpen = ref(props.menu === 'settings')
const cursorMenu = ref(props.menu === 'settings')
watch(() => props.menu, (m) => {
  menuOpen.value = m === 'settings'
  cursorMenu.value = m === 'settings'
})
watch(menuOpen, (v) => {
  if (!v) cursorMenu.value = false
})

const cursorStyle = ref<CursorStyle>('Block')
const blink = ref(true)
const copyOnSelect = ref(false)

const cursorClass = computed(() => ({
  Block: 'h-[15px] w-2 align-[-3px]',
  Bar: 'h-[15px] w-0.5 align-[-3px]',
  Underline: 'h-0.5 w-2 align-[-1px]'
}[cursorStyle.value]))

let counter = 0
function clock() {
  const d = new Date()
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

function addTab() {
  counter += 1
  const id = `zsh-${counter}`
  tabs.value.push({ id, name: 'zsh', status: '', statusColor: '#8f8f8f', lines: promptLines(clock(), '', true) })
  selected.value = id
}

function closeTab(id: string) {
  const i = tabs.value.findIndex(t => t.id === id)
  if (i < 0) return
  tabs.value.splice(i, 1)
  if (!tabs.value.length) {
    closePane('terminal')
    return
  }
  if (selected.value === id) selected.value = tabs.value[Math.max(0, i - 1)]!.id
}

function clearTab() {
  if (tab.value) tab.value.lines = promptLines(clock(), '', true)
  menuOpen.value = false
}

function restart() {
  const t = tab.value
  if (!t) return
  t.exitCode = undefined
  t.status = ''
  t.lines = [...t.lines.map(l => ({ ...l, cur: false })), { t: '' }, ...promptLines(clock(), '', true)]
}
</script>

<template>
  <section
    aria-label="Terminal"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-stretch border-b border-line pr-1.5">
      <div class="flex items-center px-3 font-display text-[13px] text-white">
        Terminal
      </div>
      <div
        role="tablist"
        aria-label="Terminal tabs"
        class="flex min-w-0 items-stretch overflow-hidden"
      >
        <PaneTab
          v-for="t in tabs"
          :key="t.id"
          :selected="t.id === selected"
          edge="left"
          class="gap-[7px] px-2.5"
          @select="selected = t.id"
          @close="closeTab(t.id)"
        >
          <HIcon
            name="term"
            :size="13"
          />{{ t.name }}<span
            class="text-[11px]"
            :style="{ color: t.statusColor }"
          >{{ t.status }}</span>
        </PaneTab>
        <button
          type="button"
          aria-label="New terminal tab"
          title="New terminal tab"
          class="flex w-9 items-center justify-center border-x border-line text-muted hover:text-white"
          @click="addTab"
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
          icon="full"
          label="Maximize"
        />
        <PaneBtn
          icon="close"
          label="Close terminal"
          @click="closePane('terminal')"
        />
      </div>
    </div>

    <div
      v-if="tab?.agent"
      class="flex items-center gap-[9px] border-b border-line bg-surface px-3 py-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="ai"
        :size="14"
        class="text-accent"
      />
      Started by the agent · background task<span class="flex-1" /><span class="text-accent">running · {{ agentElapsed }}</span>
      <button
        type="button"
        class="border border-line px-2 py-px text-[11px] text-bad hover:bg-hover"
      >
        Stop
      </button>
    </div>

    <pre class="m-0 min-h-0 flex-1 overflow-y-auto px-3.5 py-3 text-[12.5px] leading-[1.6] whitespace-pre-wrap text-toned"><span
      v-for="(l, i) in tab?.lines"
      :key="i"
      class="block min-h-[1.6em]"
      :style="l.c ? { color: l.c } : undefined"
    >{{ l.t }}<PaneCaret
      v-if="l.cur"
      :class="cursorClass"
      :blink="blink"
    /></span></pre>

    <div
      v-if="tab?.exitCode"
      class="flex items-center gap-[9px] border-t border-l-2 border-t-line border-l-bad px-3 py-2 text-[12px] text-muted"
    >
      Process exited with code <span class="text-bad">{{ tab.exitCode }}</span><span class="flex-1" />
      <button
        type="button"
        class="border border-line px-[9px] py-[2px] text-[11px] text-white hover:bg-hover"
        @click="restart"
      >
        Restart
      </button>
      <button
        type="button"
        class="border border-line px-[9px] py-[2px] text-[11px] text-white hover:bg-hover"
      >
        Send output to chat
      </button>
    </div>

    <PaneMenu v-model:open="menuOpen">
      <div
        v-if="cursorMenu"
        role="menu"
        aria-label="Cursor style"
        class="absolute top-[46px] right-[304px] z-30 menu-panel w-[170px]"
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
        class="absolute top-[46px] right-10 z-30 menu-panel w-[260px]"
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
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="copyOnSelect"
          @click="copyOnSelect = !copyOnSelect"
          @mouseenter="cursorMenu = false"
        >
          Copy on select
        </PaneMenuItem>
        <PaneMenuItem
          value="12.5"
          sub
          @mouseenter="cursorMenu = false"
        >
          Font size
        </PaneMenuItem>
        <PaneMenuItem
          value="10,000 lines"
          sub
          @mouseenter="cursorMenu = false"
        >
          Scrollback
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          kbd="meta+k"
          @click="clearTab"
          @mouseenter="cursorMenu = false"
        >
          Clear
        </PaneMenuItem>
        <PaneMenuItem
          @click="menuOpen = false"
          @mouseenter="cursorMenu = false"
        >
          Rename tab
        </PaneMenuItem>
        <PaneMenuItem
          danger
          @click="menuOpen = false"
          @mouseenter="cursorMenu = false"
        >
          Kill process
        </PaneMenuItem>
      </div>
    </PaneMenu>
  </section>
</template>
