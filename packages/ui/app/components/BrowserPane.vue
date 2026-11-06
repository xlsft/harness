<script setup lang="ts">
// Browser pane: the session's real browser (a Chromium on the client's machine, ~/lib/browser), shared with the
// agent's Browser* tools. Frames of the active tab are drawn on a canvas; mouse, wheel, keys and paste go back to the
// page. Tabs, address, back/forward/reload and the viewport (fits the pane, or a tablet or phone size) are the
// browser's own. While the agent acts here a bar says what it does and its target is outlined; Take over makes its
// action tools wait for you. Without a browser on the machine the pane offers to download one.
import { usePanes } from '~/composables/usePanes'
import { modifierMask } from '~/lib/browser'
import type { BrowserConnection, BrowserConnectionState } from '~/lib/browser/connection'
import type { BrowserAvailabilityVM, BrowserFrame, BrowserStateVM, ViewportPreset } from '~/lib/browser/types'
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState, menu: string }>()

const store = useSessionStore()
const toast = useToast()
const { closePane } = usePanes()

const remote = computed(() => !!props.session.meta.remote)
const api = () => store.transport!.browser()

const availability = ref<BrowserAvailabilityVM | null>(null)
const installing = ref<number | null>(null)
const state = ref<BrowserStateVM | null>(null)
const link = ref<BrowserConnectionState>('idle')
const error = ref('')
const address = ref('')
const editing = ref(false)
let conn: BrowserConnection | null = null

const tabs = computed(() => state.value?.tabs ?? [])
const tab = computed(() => tabs.value.find(t => t.id === state.value?.activeTabId) ?? tabs.value[0])
const blank = computed(() => !tab.value?.url || tab.value.url === 'about:blank')
const takenOver = computed(() => !!state.value?.takenOver)
const realChrome = computed(() => !!state.value?.realChrome)
const recording = computed(() => !!state.value?.recording)
const chromeAvailable = ref(false)

async function refreshChrome() {
  try {
    chromeAvailable.value = (await api().realChrome(props.session.meta.id)).available
  } catch { chromeAvailable.value = false }
}

// Moves the session's browser to the user's Chrome or back; the view reconnects to what runs now.
async function switchChrome(on: boolean) {
  error.value = ''
  try {
    await api().useRealChrome(props.session.meta.id, on)
    disconnect()
    connect()
    void refreshChrome()
  } catch (e) {
    error.value = message(e)
  }
}

async function toggleRecording() {
  error.value = ''
  try {
    await api().record(props.session.meta.id, !recording.value)
  } catch (e) {
    error.value = message(e)
  }
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

// ── frames ──
const canvas = ref<HTMLCanvasElement | null>(null)
const stage = ref<HTMLElement | null>(null)
const frame = ref<Pick<BrowserFrame, 'width' | 'height'> | null>(null)
let drawing = false
let latest: BrowserFrame | null = null

async function draw() {
  if (drawing) return
  drawing = true
  while (latest) {
    const f = latest
    latest = null
    try {
      const bitmap = await createImageBitmap(new Blob([f.data], { type: f.mime || 'image/jpeg' }))
      const c = canvas.value
      if (c) {
        if (c.width !== bitmap.width || c.height !== bitmap.height) {
          c.width = bitmap.width
          c.height = bitmap.height
        }
        c.getContext('2d')?.drawImage(bitmap, 0, 0)
      }
      bitmap.close()
      frame.value = { width: f.width, height: f.height }
    } catch { /* a broken frame: the next one replaces it */ }
  }
  drawing = false
}

// The page drawn inside the stage, keeping its proportions.
const stageSize = ref({ width: 0, height: 0 })
const drawn = computed(() => {
  const f = frame.value
  const { width, height } = stageSize.value
  if (!f || !width || !height) return { width: 0, height: 0 }
  const scale = Math.min(1, width / f.width, height / f.height)
  return { width: f.width * scale, height: f.height * scale }
})

// ── connection ──
function connect(retryOpen = true) {
  disconnect()
  error.value = ''
  if (remote.value || !store.transport || !availability.value?.available) return
  const c = api().open(props.session.meta.id, viewportOption())
  conn = c
  c.onFrame((f) => {
    latest = f
    void draw()
  })
  c.onState((s) => {
    state.value = s
    if (!editing.value) address.value = tab.value?.url === 'about:blank' ? '' : tab.value?.url ?? ''
  })
  c.onError(m => (error.value = m))
  c.onConnectionChange(s => (link.value = s))
  c.connect().catch(async (e) => {
    // The client restarted: reopen the session, then connect again.
    if (retryOpen && /not open/i.test(message(e))) {
      await store.resume(props.session.meta.id).then(() => connect(false)).catch(err => (error.value = message(err)))
    } else error.value = message(e)
  })
}
function disconnect() {
  conn?.close()
  conn = null
  state.value = null
  frame.value = null
  latest = null
}

async function start() {
  if (remote.value || !store.transport) return
  try {
    availability.value = await api().availability()
    connect()
    void refreshChrome()
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(start)
onBeforeUnmount(disconnect)
watch(() => props.session.meta.id, start)

function install() {
  installing.value = 0
  const h = api().install((got, total) => (installing.value = total ? got / total : 0))
  h.done.then((a) => {
    if (a) availability.value = a
    connect()
  }).catch(e => (error.value = message(e))).finally(() => (installing.value = null))
}

// ── viewport ──
const PRESETS: { key: ViewportPreset, label: string }[] = [
  { key: 'custom', label: 'Fit' },
  { key: 'tablet', label: 'Tablet' },
  { key: 'mobile', label: 'Phone' }
]
const preset = ref<ViewportPreset>('custom')
const viewportBar = ref(false)
function fitSize() {
  return { width: Math.max(320, Math.round(stageSize.value.width)), height: Math.max(240, Math.round(stageSize.value.height)) }
}
function viewportOption() {
  if (preset.value !== 'custom') return { viewport: { preset: preset.value } }
  const s = fitSize()
  return s.width > 320 ? { viewport: { preset: 'custom' as const, ...s } } : {}
}
function pickViewport(p: ViewportPreset) {
  preset.value = p
  if (p === 'custom') {
    const s = fitSize()
    conn?.setViewport('custom', s.width, s.height)
  } else conn?.setViewport(p)
}
let resizeTimer: ReturnType<typeof setTimeout> | undefined
let observer: ResizeObserver | null = null
onMounted(() => {
  observer = new ResizeObserver(([e]) => {
    if (!e) return
    stageSize.value = { width: e.contentRect.width, height: e.contentRect.height }
    if (preset.value !== 'custom') return
    if (resizeTimer) clearTimeout(resizeTimer)
    resizeTimer = setTimeout(() => {
      const s = fitSize()
      conn?.setViewport('custom', s.width, s.height)
    }, 200)
  })
  if (stage.value) observer.observe(stage.value)
})
onBeforeUnmount(() => {
  observer?.disconnect()
  if (resizeTimer) clearTimeout(resizeTimer)
})

// ── navigation ──
function go() {
  let u = address.value.trim()
  if (!u) return
  if (!/^[a-z][a-z0-9+.-]*:/i.test(u)) u = /^(localhost|127\.|\[::1\])/.test(u) ? `http://${u}` : /\s/.test(u) || !u.includes('.') ? `https://duckduckgo.com/?q=${encodeURIComponent(u)}` : `https://${u}`
  conn?.navigate(u)
  editing.value = false
  canvas.value?.focus()
}
const tabLetter = (t: { title: string, url: string }) => (t.title || t.url.replace(/^\w+:\/\/(www\.)?/, '') || '·').charAt(0).toUpperCase()

// ── input ──
function point(e: MouseEvent) {
  const f = frame.value
  const r = canvas.value?.getBoundingClientRect()
  if (!f || !r?.width) return null
  return { x: ((e.clientX - r.left) / r.width) * f.width, y: ((e.clientY - r.top) / r.height) * f.height }
}
const BUTTONS = ['left', 'middle', 'right'] as const
function mouse(event: 'down' | 'up', e: MouseEvent) {
  const p = point(e)
  if (!p) return
  if (event === 'down') canvas.value?.focus()
  conn?.mouse({ event, ...p, button: BUTTONS[e.button] ?? 'left', clickCount: e.detail || 1, modifiers: modifierMask(e) })
}
let moving = false
function onMove(e: MouseEvent) {
  if (moving) return
  moving = true
  requestAnimationFrame(() => {
    moving = false
    const p = point(e)
    if (p) conn?.mouse({ event: 'move', ...p, button: e.buttons & 1 ? 'left' : 'none', modifiers: modifierMask(e) })
  })
}
function onWheel(e: WheelEvent) {
  const p = point(e)
  if (p) conn?.mouse({ event: 'wheel', ...p, deltaX: e.deltaX, deltaY: e.deltaY, modifiers: modifierMask(e) })
}
function onKey(event: 'down' | 'up', e: KeyboardEvent) {
  // ⌘ shortcuts stay with the app, except paste (the paste event sends the text).
  if (e.metaKey) return
  e.preventDefault()
  conn?.key({ event, key: e.key, code: e.code, text: event === 'down' && e.key.length === 1 ? e.key : undefined, keyCode: e.keyCode, modifiers: modifierMask(e) })
}
function onPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text')
  if (text) conn?.paste(text)
}

// ── the agent ──
const action = computed(() => props.session.browserAction)
const acting = computed(() => action.value?.phase === 'started')
const target = computed(() => {
  const r = action.value?.rect
  const f = frame.value
  if (!acting.value || !r || !f) return null
  return { left: `${(r.x / f.width) * 100}%`, top: `${(r.y / f.height) * 100}%`, width: `${(r.width / f.width) * 100}%`, height: `${(r.height / f.height) * 100}%` }
})

// ── menu ──
const menuOpen = ref(props.menu === 'settings')
async function clearData() {
  menuOpen.value = false
  try {
    await api().clearData(props.session.meta.id)
    toast.add({ title: 'Browsing data cleared', color: 'neutral' })
  } catch (e) {
    error.value = message(e)
  }
}
async function stopBrowser() {
  menuOpen.value = false
  disconnect()
  try {
    await api().close(props.session.meta.id)
  } catch (e) {
    error.value = message(e)
  }
  closePane('browser')
}
async function copyUrl() {
  menuOpen.value = false
  if (tab.value?.url) await navigator.clipboard.writeText(tab.value.url)
}
</script>

<template>
  <section
    aria-label="Browser"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-stretch border-b border-line pr-1.5">
      <div
        role="tablist"
        aria-label="Browser tabs"
        class="flex min-w-0 items-stretch overflow-hidden"
      >
        <PaneTab
          v-for="t in tabs"
          :key="t.id"
          :selected="t.active"
          class="max-w-[200px] gap-2 px-3"
          :title="t.url"
          @select="conn?.switchTab(t.id)"
          @close="conn?.closeTab(t.id)"
        >
          <HIcon
            v-if="t.loading"
            name="loading"
            spin
            :size="13"
            class="text-accent"
          />
          <span
            v-else
            class="flex size-3.5 shrink-0 items-center justify-center bg-raised text-[9px] text-strong not-italic"
          >{{ tabLetter(t) }}</span>
          <span class="truncate">{{ t.title || t.url || 'New tab' }}</span>
          <span
            v-if="t.consoleErrors"
            class="shrink-0 text-[10.5px] text-bad"
            :title="`${t.consoleErrors} console errors`"
          >{{ t.consoleErrors }}</span>
        </PaneTab>
        <button
          v-if="state"
          type="button"
          aria-label="New browser tab"
          title="New browser tab"
          class="flex w-9 shrink-0 items-center justify-center rounded-none border-r border-line text-muted hover:text-strong"
          @click="conn?.newTab()"
        >
          <HIcon name="plus" />
        </button>
      </div>
      <span class="flex-1" />
      <div class="flex items-center gap-[2px]">
        <PaneBtn
          icon="more"
          label="Browser settings"
          white
          :pressed="menuOpen"
          data-menu-anchor
          @click="menuOpen = !menuOpen"
        />
        <PaneBtn
          icon="close"
          label="Close browser"
          @click="closePane('browser')"
        />
      </div>
    </div>

    <div class="flex h-[44px] shrink-0 items-center gap-1 border-b border-line px-2">
      <PaneBtn
        icon="arrow-left"
        label="Back"
        :disabled="!tab?.canGoBack"
        @click="conn?.back()"
      />
      <PaneBtn
        icon="arrow-right"
        label="Forward"
        :disabled="!tab?.canGoForward"
        @click="conn?.forward()"
      />
      <PaneBtn
        :icon="tab?.loading ? 'close' : 'reload'"
        :label="tab?.loading ? 'Stop' : 'Reload'"
        :disabled="!state"
        @click="tab?.loading ? conn?.stop() : conn?.reload()"
      />
      <label class="flex h-[30px] min-w-0 flex-1 items-center gap-2 border border-line bg-surface px-2.5 focus-within:border-accent">
        <HIcon
          name="globe"
          :size="13"
          class="text-muted"
        />
        <span class="sr-only">Address</span>
        <input
          v-model="address"
          type="text"
          spellcheck="false"
          autocapitalize="off"
          autocorrect="off"
          placeholder="Type a URL"
          class="min-w-0 flex-1 truncate bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
          :disabled="!state"
          @focus="editing = true"
          @blur="editing = false"
          @keydown.enter="go"
        >
      </label>
      <PaneBtn
        icon="play"
        :label="recording ? 'Stop recording' : 'Record as GIF'"
        :pressed="recording"
        :disabled="!state"
        @click="toggleRecording"
      />
      <PaneBtn
        icon="phone"
        label="Viewport"
        :pressed="viewportBar"
        :disabled="!state"
        @click="viewportBar = !viewportBar"
      />
    </div>

    <BrowserPreviewBar
      v-if="!remote && store.transport"
      :session-id="session.meta.id"
      :api="api()"
    />

    <div
      v-if="realChrome"
      class="flex shrink-0 items-center gap-[9px] border-b border-line bg-surface px-3 py-1.5 text-[12px] text-muted"
      role="status"
    >
      <HIcon
        name="globe"
        :size="14"
        class="shrink-0 text-warn"
      />
      <span class="min-w-0 flex-1 truncate">In your own Chrome, with your sign-ins. The agent reaches only sites you allow.</span>
      <HBtn @click="switchChrome(false)">
        Back to its own browser
      </HBtn>
    </div>
    <div
      v-else-if="chromeAvailable && !remote"
      class="flex shrink-0 items-center gap-[9px] border-b border-line px-3 py-1 text-[12px] text-muted"
    >
      <span class="min-w-0 flex-1 truncate">Chrome is running with remote debugging on.</span>
      <HBtn @click="switchChrome(true)">
        Use my Chrome
      </HBtn>
    </div>
    <p
      v-if="state?.lastRecording && !recording"
      class="shrink-0 truncate border-b border-line px-3 py-1 text-[12px] text-muted"
      role="status"
    >
      Recording saved: {{ state.lastRecording }}
    </p>

    <!-- What the agent is doing here -->
    <div
      v-if="acting || takenOver"
      class="flex shrink-0 items-center gap-[9px] border-b border-line bg-surface px-3 py-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="ai"
        :size="14"
        class="shrink-0 text-accent"
      />
      <span class="shrink-0 text-strong">{{ takenOver ? 'You have the browser' : 'The agent is using the browser' }}</span>
      <span
        v-if="!takenOver && action"
        class="min-w-0 truncate"
      >· step {{ action.step }} · {{ action.label }}</span>
      <span class="flex-1" />
      <HBtn @click="conn?.takeover(!takenOver)">
        {{ takenOver ? 'Hand back' : 'Take over' }}
      </HBtn>
      <HBtn
        v-if="!takenOver"
        tone="danger"
        @click="store.interrupt(session.meta.id)"
      >
        Stop
      </HBtn>
    </div>

    <div
      v-if="viewportBar"
      class="flex shrink-0 items-center justify-center gap-1 border-b border-line px-3 py-[5px] text-[11.5px] text-muted"
    >
      <button
        v-for="p in PRESETS"
        :key="p.key"
        type="button"
        class="px-2 py-0.5 hover:text-strong"
        :class="preset === p.key && 'bg-raised text-strong'"
        @click="pickViewport(p.key)"
      >
        {{ p.label }}
      </button>
      <span
        v-if="frame"
        class="ml-2 tabular-nums"
      >{{ frame.width }} × {{ frame.height }}</span>
    </div>

    <p
      v-if="error"
      role="alert"
      class="m-0 flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-[12px] text-bad"
    >
      <span class="min-w-0 flex-1 truncate">{{ error }}</span>
      <HBtn @click="start">
        Try again
      </HBtn>
    </p>
    <p
      v-else-if="link === 'reconnecting'"
      class="m-0 shrink-0 border-b border-line px-3 py-1.5 text-[12px] text-warn"
    >
      Reconnecting to the browser…
    </p>

    <div
      ref="stage"
      class="relative flex min-h-0 flex-1 items-start justify-center overflow-hidden"
    >
      <!-- The page -->
      <div
        v-show="state && !blank && frame"
        class="relative shrink-0"
        :style="{ width: `${drawn.width}px`, height: `${drawn.height}px` }"
      >
        <canvas
          ref="canvas"
          tabindex="0"
          aria-label="Page"
          class="block size-full outline-none"
          @mousedown.prevent="mouse('down', $event)"
          @mouseup="mouse('up', $event)"
          @mousemove="onMove"
          @wheel.prevent="onWheel"
          @contextmenu.prevent
          @keydown="onKey('down', $event)"
          @keyup="onKey('up', $event)"
          @paste.prevent="onPaste"
        />
        <div
          v-if="target"
          aria-hidden="true"
          class="pointer-events-none absolute outline-2 outline-offset-2 outline-accent"
          :style="target"
        />
      </div>

      <!-- Nothing to show yet -->
      <div
        v-if="remote"
        class="absolute inset-0 flex items-center justify-center p-6 text-center text-[12.5px] text-muted"
      >
        This session runs on {{ session.meta.deviceName || 'another computer' }}; its browser opens there.
      </div>
      <div
        v-else-if="availability && !availability.available"
        class="absolute inset-0 flex flex-col items-center justify-center gap-2.5 p-6 text-center"
      >
        <HIcon
          name="globe"
          :size="30"
          class="text-muted"
        />
        <span class="font-display text-[15px] text-strong">No browser found on this computer</span>
        <span class="max-w-[340px] text-[12.5px] leading-[1.55] text-muted">Install Chrome, Chromium, Edge or Brave, or download a private copy of Chrome for Testing for harness to use.</span>
        <span
          v-if="installing !== null"
          class="mt-1 flex w-[220px] items-center gap-2 text-[11.5px] text-muted tabular-nums"
        >
          <span class="h-1.5 flex-1 bg-raised"><span
            class="block h-full bg-accent"
            :style="{ width: `${Math.round(installing * 100)}%` }"
          /></span>{{ Math.round(installing * 100) }}%
        </span>
        <HBtn
          v-else-if="availability.downloadable"
          size="md"
          variant="primary"
          @click="install"
        >
          Download Chrome for Testing {{ availability.downloadVersion }}
        </HBtn>
      </div>
      <div
        v-else-if="state && blank"
        class="absolute inset-0 flex flex-col items-center justify-center gap-2.5 p-6 text-center"
      >
        <HIcon
          name="globe"
          :size="30"
          class="text-muted"
        />
        <span class="font-display text-[15px] text-strong">Browse with the agent</span>
        <span class="max-w-[340px] text-[12.5px] leading-[1.55] text-muted">Type a URL, or ask the agent to open, click and check pages. It sees this same browser, and asks before acting on a new site.</span>
      </div>
      <div
        v-else-if="!state && !error"
        class="absolute inset-0 flex items-center justify-center"
        role="status"
        aria-label="Starting the browser"
      >
        <HMark :width="28" />
      </div>
    </div>

    <PaneMenu v-model:open="menuOpen">
      <div
        role="menu"
        aria-label="Browser settings"
        class="absolute top-[46px] right-10 z-30 menu-panel w-[220px]"
      >
        <PaneMenuItem
          :disabled="!tab?.url"
          @click="copyUrl"
        >
          Copy page address
        </PaneMenuItem>
        <PaneMenuItem @click="clearData">
          Clear browsing data
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          danger
          @click="stopBrowser"
        >
          Stop browser
        </PaneMenuItem>
      </div>
    </PaneMenu>
  </section>
</template>
