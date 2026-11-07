<script setup lang="ts">
// The iOS Simulator pane (docs/design/demo-recording.md): the simulators of this Mac, boot or shut one down, and the
// booted one streamed as frames. A click on the frame is a tap, a drag a swipe, a long press holds; keys typed while
// the frame has focus go to the device. The client does the input through SimulatorKit, headless, so Simulator.app
// need not be open. Device-local: a session of another machine gets a note instead.
import type { SessionState } from '~/lib/session/types'
import type { SimulatorDeviceVM, SimulatorFrameVM, SimulatorInputVM } from '~/lib/transport/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const { closePane } = usePanes()

const remote = computed(() => !!props.session.meta.remote)
const devices = ref<SimulatorDeviceVM[]>([])
const unavailable = ref('')
const error = ref('')
const loading = ref(false)
const busy = ref(false)
const udid = ref('')
const frame = ref<SimulatorFrameVM | null>(null)

const device = computed(() => devices.value.find(d => d.udid === udid.value))
const booted = computed(() => device.value?.state === 'Booted')
const options = computed(() => devices.value.map(d => ({ value: d.udid, label: d.name, hint: `${d.runtimeName} · ${d.state}` })))

function errorText(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function refresh() {
  if (remote.value || !store.transport?.listSimulators) {
    unavailable.value = tt('The iOS Simulator can only be used from the machine that runs this session.')
    return
  }
  loading.value = true
  try {
    const r = await store.transport.listSimulators()
    devices.value = r.devices
    unavailable.value = r.unavailable
    if (!devices.value.some(d => d.udid === udid.value)) udid.value = (devices.value.find(d => d.state === 'Booted') ?? devices.value[0])?.udid ?? ''
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}

onMounted(refresh)

async function act(fn: () => Promise<void> | undefined) {
  busy.value = true
  error.value = ''
  try {
    await fn()
    await refresh()
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}

const boot = () => act(() => store.transport?.bootSimulator?.(udid.value))
const shutdown = () => act(() => store.transport?.shutdownSimulator?.(udid.value))

// The frame stream of the booted device; the previous frame's blob URL is let go when a new one arrives.
let watching: AbortController | null = null
function setFrame(f: SimulatorFrameVM | null) {
  if (frame.value?.url.startsWith('blob:')) URL.revokeObjectURL(frame.value.url)
  frame.value = f
}
function stopWatching() {
  watching?.abort()
  watching = null
  setFrame(null)
}
watch([udid, booted], () => {
  stopWatching()
  if (!booted.value || !store.transport?.watchSimulator) return
  const ctl = new AbortController()
  watching = ctl
  const id = udid.value
  store.transport.watchSimulator(id, (f) => {
    if (!ctl.signal.aborted) setFrame(f)
    else if (f.url.startsWith('blob:')) URL.revokeObjectURL(f.url)
  }, ctl.signal).catch((e) => {
    if (!ctl.signal.aborted) error.value = errorText(e)
  })
}, { immediate: true })
onBeforeUnmount(stopWatching)

// The agent's latest action on this device, shown for a few seconds.
const now = ref(Date.now())
const tick = setInterval(() => (now.value = Date.now()), 1000)
onBeforeUnmount(() => clearInterval(tick))
const activity = computed(() => {
  const a = props.session.simulatorActivity
  return a && a.udid === udid.value && now.value - a.at < 4000 ? a.action : ''
})

async function send(input: SimulatorInputVM) {
  if (!booted.value) return
  try {
    await store.transport?.simulatorInput?.(udid.value, input)
  } catch (e) {
    error.value = errorText(e)
  }
}

// Pointer → tap, long press or swipe, as fractions of the frame.
const img = ref<HTMLImageElement | null>(null)
let press: { x: number, y: number, t: number, px: number, py: number } | null = null
function fraction(e: PointerEvent) {
  const r = img.value!.getBoundingClientRect()
  return { x: Math.min(Math.max((e.clientX - r.left) / r.width, 0), 1), y: Math.min(Math.max((e.clientY - r.top) / r.height, 0), 1) }
}
function onDown(e: PointerEvent) {
  if (e.button !== 0 || !img.value) return
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  const p = fraction(e)
  press = { ...p, t: performance.now(), px: e.clientX, py: e.clientY }
}
function onUp(e: PointerEvent) {
  if (!press || !img.value) return
  const start = press
  press = null
  const held = Math.round(performance.now() - start.t)
  const end = fraction(e)
  if (Math.hypot(e.clientX - start.px, e.clientY - start.py) < 6) {
    send({ kind: 'tap', x: start.x, y: start.y, durationMs: held > 500 ? held : 0 })
  } else {
    send({ kind: 'swipe', fromX: start.x, fromY: start.y, toX: end.x, toY: end.y, durationMs: Math.min(Math.max(held, 100), 2000) })
  }
}

// Keys while the frame has focus: characters are batched into one text input, named keys and shortcuts go as keys.
const NAMED: Record<string, string> = {
  Enter: 'return', Backspace: 'delete', Delete: 'forward_delete', Tab: 'tab', Escape: 'escape',
  ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down', Home: 'home', End: 'end', PageUp: 'page_up', PageDown: 'page_down'
}
let pending = ''
let flushTimer: ReturnType<typeof setTimeout> | undefined
function flush() {
  clearTimeout(flushTimer)
  if (pending) send({ kind: 'text', text: pending })
  pending = ''
}
function onKey(e: KeyboardEvent) {
  if (!booted.value || e.isComposing) return
  const mods = [e.ctrlKey && 'ctrl', e.altKey && 'alt', e.metaKey && 'cmd'].filter(Boolean) as string[]
  if (NAMED[e.key] || mods.length) {
    const key = NAMED[e.key] ?? (e.key.length === 1 ? e.key.toLowerCase() : '')
    if (!key) return
    e.preventDefault()
    flush()
    send({ kind: 'keys', keys: [...mods, ...(e.shiftKey && mods.length ? ['shift'] : []), key].join('+') })
    return
  }
  if (e.key.length !== 1) return
  e.preventDefault()
  pending += e.key
  clearTimeout(flushTimer)
  flushTimer = setTimeout(flush, 150)
}
onBeforeUnmount(() => clearTimeout(flushTimer))
</script>

<template>
  <section
    :aria-label="tt('Simulator')"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="min-w-0 flex-1 truncate font-display text-[13px] text-strong">{{ tt('Simulator') }}</span>
      <PaneBtn
        icon="reload"
        :label="tt('Refresh')"
        :disabled="loading || remote"
        @click="refresh"
      />
      <PaneFullBtn />
      <PaneBtn
        icon="close"
        :label="tt('Close')"
        @click="closePane('simulator')"
      />
    </div>
    <div
      v-if="!unavailable && devices.length"
      class="flex h-[44px] shrink-0 items-center gap-2 border-b border-line px-2"
    >
      <HSelect
        v-model="udid"
        :options="options"
        :aria-label="tt('Device')"
        class="min-w-0 flex-1"
      />
      <HBtn
        v-if="!booted"
        :disabled="busy || !udid"
        @click="boot"
      >
        {{ busy ? tt('Booting…') : tt('Boot') }}
      </HBtn>
      <template v-else>
        <PaneBtn
          icon="dot"
          :label="tt('Home')"
          @click="send({ kind: 'button', button: 'home' })"
        />
        <PaneBtn
          icon="lock"
          :label="tt('Lock')"
          @click="send({ kind: 'button', button: 'lock' })"
        />
        <HBtn
          :disabled="busy"
          @click="shutdown"
        >
          {{ tt('Shut down') }}
        </HBtn>
      </template>
    </div>
    <p
      v-if="error"
      role="alert"
      class="m-0 border-b border-line px-3 py-1.5 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <div class="relative flex min-h-0 flex-1 items-center justify-center overflow-hidden p-3">
      <span
        v-if="unavailable"
        class="max-w-[360px] text-center text-[12.5px] text-muted"
      >{{ unavailable }}</span>
      <HLoader v-else-if="loading && !devices.length" />
      <span
        v-else-if="!devices.length"
        class="text-center text-[12.5px] text-muted"
      >{{ tt('No simulators on this Mac. Add one in Xcode > Window > Devices and Simulators.') }}</span>
      <span
        v-else-if="!booted"
        class="text-center text-[12.5px] text-muted"
      >{{ tt('Boot the device to see its screen.') }}</span>
      <HLoader v-else-if="!frame" />
      <div
        v-else
        tabindex="0"
        :aria-label="tt('Simulator screen: click to tap, drag to swipe, type to send keys')"
        class="flex h-full w-full cursor-pointer items-center justify-center outline-none focus-visible:outline-1 focus-visible:outline-accent"
        @pointerdown="onDown"
        @pointerup="onUp"
        @keydown="onKey"
      >
        <img
          ref="img"
          :src="frame.url"
          :width="frame.width"
          :height="frame.height"
          alt=""
          draggable="false"
          class="h-auto max-h-full w-auto max-w-full select-none"
        >
      </div>
      <span
        v-if="activity"
        role="status"
        class="absolute top-3 left-3 border border-l-2 border-default border-l-accent bg-surface px-2 py-1 text-[11.5px] text-toned"
      >{{ tt('Agent: {0}', [activity]) }}</span>
    </div>
  </section>
</template>
