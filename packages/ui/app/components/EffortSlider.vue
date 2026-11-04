<script setup lang="ts">
// Effort as a slider over the levels the model or agent offers (lowest first, an agent's in its order): a thick track
// with a square thumb inside it. Click, drag or use the arrow keys. The recommended level, if any, has a taller tick and
// says so in the label.
import { effortSliderLabel, type EffortLevel } from '~/utils/composer'

const props = defineProps<{ levels: EffortLevel[], recommended?: string }>()
const model = defineModel<string>({ required: true })

const index = computed(() => Math.max(0, props.levels.findIndex(l => l.id === model.value)))
const shown = computed(() => effortSliderLabel(props.levels, model.value, props.recommended))
const last = computed(() => props.levels.length - 1)
// The thumb's left edge, as a share of the track minus the thumb.
const share = (i: number) => (last.value ? i / last.value : 0)
const atMax = computed(() => last.value > 0 && index.value === last.value)

const track = ref<HTMLElement | null>(null)
function pickAt(clientX: number) {
  const r = track.value?.getBoundingClientRect()
  if (!r || !props.levels.length) return
  const i = Math.round(Math.min(1, Math.max(0, (clientX - r.left) / r.width)) * last.value)
  model.value = props.levels[i]!.id
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
  model.value = props.levels[Math.min(last.value, Math.max(0, index.value + step))]!.id
}
</script>

<template>
  <div class="flex w-[220px] flex-col gap-2 p-2.5">
    <div class="flex items-baseline justify-between text-[11.5px]">
      <span class="text-muted">Effort</span>
      <span :class="atMax ? 'text-accent' : 'text-strong'">{{ shown.label }}<span
        v-if="shown.recommended"
        class="text-muted"
      > · Recommended</span></span>
    </div>
    <div
      ref="track"
      role="slider"
      tabindex="0"
      aria-label="Effort"
      :aria-valuemin="0"
      :aria-valuemax="last"
      :aria-valuenow="index"
      :aria-valuetext="shown.recommended ? `${shown.label}, recommended` : shown.label"
      class="relative h-3.5 cursor-pointer touch-none overflow-hidden rounded-control bg-line outline-none focus-visible:outline focus-visible:outline-offset-2 focus-visible:outline-accent"
      @pointerdown="onDown"
      @keydown="onKey"
    >
      <!-- Fill up to the thumb's right edge. -->
      <div
        class="absolute inset-y-0 left-0 rounded-control bg-accent transition-[width] duration-150"
        :style="{ width: `calc((100% - 14px) * ${share(index)} + 14px)` }"
      />
      <!-- A tick at each level, where the thumb's middle stops. -->
      <span
        v-for="(l, i) in levels"
        :key="l.id"
        aria-hidden="true"
        class="absolute top-1/2 w-px -translate-x-1/2 -translate-y-1/2"
        :class="[i < index ? 'bg-black/45' : 'bg-(--color-dimmed)', l.id === recommended ? 'h-2.5' : 'h-1.5']"
        :style="{ left: `calc((100% - 14px) * ${share(i)} + 7px)` }"
      />
      <span
        class="absolute inset-y-0 w-3.5 rounded-control bg-strong transition-[left] duration-150"
        :style="{ left: `calc((100% - 14px) * ${share(index)})` }"
      />
    </div>
  </div>
</template>
