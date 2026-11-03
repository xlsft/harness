<script setup lang="ts">
// Effort as a slider over the levels the model offers (lowest first): a thick track with a square thumb inside
// it. Click, drag or use the arrow keys. At the top level the filled part turns into a field of small squares
// that fade in and out.
import type { EffortName } from '~/lib/session/types'

const props = defineProps<{ levels: EffortName[] }>()
const model = defineModel<EffortName>({ required: true })

const LABEL: Record<EffortName, string> = { low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' }

const index = computed(() => Math.max(0, props.levels.indexOf(model.value)))
const last = computed(() => props.levels.length - 1)
// The thumb's left edge, as a share of the track minus the thumb.
const share = (i: number) => (last.value ? i / last.value : 0)
const atMax = computed(() => last.value > 0 && index.value === last.value)

// Top-level animation, drawn on a canvas over the fill: fire spreading from the left edge. A pixel-fire
// automaton turned sideways: the left column burns at full heat and each tick every cell takes its left
// neighbour's heat (from a random row nearby) minus a random decay, so a dense, flickering flame licks to
// the right and dies out raggedly. Heat brightens the fill. Runs only while shown at the top level, and not
// at all with reduced motion.
const sparks = ref<HTMLCanvasElement | null>(null)
const CELL = 2
const TICK_MS = 40
let raf = 0

function animate() {
  const cv = sparks.value
  const ctx = cv?.getContext('2d')
  if (!cv || !ctx || matchMedia('(prefers-reduced-motion: reduce)').matches) return
  const dpr = window.devicePixelRatio || 1
  const w = cv.clientWidth
  const h = cv.clientHeight
  cv.width = Math.round(w * dpr)
  cv.height = Math.round(h * dpr)
  ctx.scale(dpr, dpr)
  const cols = Math.ceil(w / CELL)
  const rows = Math.ceil(h / CELL)
  // Average decay per cell, so the flame reaches about 85% of the track.
  const decay = 2 / (cols * 0.85)
  const heat = new Float32Array(cols * rows)
  let last = 0
  const frame = (t: number) => {
    raf = requestAnimationFrame(frame)
    if (t - last < TICK_MS) return
    last = t
    for (let y = 0; y < rows; y++) heat[y * cols] = 0.85 + Math.random() * 0.15
    for (let x = cols - 1; x > 0; x--) {
      for (let y = 0; y < rows; y++) {
        const from = Math.min(rows - 1, Math.max(0, y + Math.round(Math.random() * 2 - 1)))
        heat[y * cols + x] = Math.max(0, heat[from * cols + x - 1]! - Math.random() * decay)
      }
    }
    ctx.clearRect(0, 0, w, h)
    for (let y = 0; y < rows; y++) {
      for (let x = 0; x < cols; x++) {
        const v = heat[y * cols + x]!
        if (v < 0.04) continue
        ctx.fillStyle = `rgba(214, 232, 255, ${(v * 0.55).toFixed(3)})`
        ctx.fillRect(x * CELL, y * CELL, CELL, CELL)
      }
    }
  }
  raf = requestAnimationFrame(frame)
}

watch(atMax, (on) => {
  cancelAnimationFrame(raf)
  if (on) nextTick(animate)
}, { immediate: true })
onBeforeUnmount(() => cancelAnimationFrame(raf))

const track = ref<HTMLElement | null>(null)
function pickAt(clientX: number) {
  const r = track.value?.getBoundingClientRect()
  if (!r || !props.levels.length) return
  const i = Math.round(Math.min(1, Math.max(0, (clientX - r.left) / r.width)) * last.value)
  model.value = props.levels[i]!
}
function onDown(e: PointerEvent) {
  pickAt(e.clientX)
  const move = (ev: PointerEvent) => pickAt(ev.clientX)
  const up = () => {
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
  }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}
function onKey(e: KeyboardEvent) {
  const step = e.key === 'ArrowRight' || e.key === 'ArrowUp' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowDown' ? -1 : 0
  if (!step) return
  e.preventDefault()
  model.value = props.levels[Math.min(last.value, Math.max(0, index.value + step))]!
}
</script>

<template>
  <div class="flex w-[220px] flex-col gap-2 p-2.5">
    <div class="flex items-baseline justify-between text-[11.5px]">
      <span class="text-muted">Effort</span>
      <span :class="atMax ? 'text-accent' : 'text-white'">{{ LABEL[model] }}</span>
    </div>
    <div
      ref="track"
      role="slider"
      tabindex="0"
      aria-label="Effort"
      :aria-valuemin="0"
      :aria-valuemax="last"
      :aria-valuenow="index"
      :aria-valuetext="LABEL[model]"
      class="relative h-3.5 cursor-pointer touch-none overflow-hidden bg-line outline-none focus-visible:outline focus-visible:outline-offset-2 focus-visible:outline-accent"
      @pointerdown="onDown"
      @keydown="onKey"
    >
      <!-- Fill up to the thumb's right edge. -->
      <div
        class="absolute inset-y-0 left-0 overflow-hidden bg-accent transition-[width] duration-150"
        :style="{ width: `calc((100% - 14px) * ${share(index)} + 14px)` }"
      >
        <canvas
          v-if="atMax"
          ref="sparks"
          aria-hidden="true"
          class="absolute inset-0 size-full"
        />
      </div>
      <span
        class="absolute inset-y-0 w-3.5 border-2 border-default bg-white transition-[left] duration-150"
        :style="{ left: `calc((100% - 14px) * ${share(index)})` }"
      />
    </div>
  </div>
</template>
