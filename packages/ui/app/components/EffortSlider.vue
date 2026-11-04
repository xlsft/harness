<script setup lang="ts">
// Effort as a slider over the levels the model offers (lowest first): a thick track with a square thumb inside
// it. Click, drag or use the arrow keys.
import type { EffortName } from '~/lib/session/types'
import { EFFORT_LABEL } from '~/utils/composer'

const props = defineProps<{ levels: EffortName[] }>()
const model = defineModel<EffortName>({ required: true })

const LABEL = EFFORT_LABEL

const index = computed(() => Math.max(0, props.levels.indexOf(model.value)))
const last = computed(() => props.levels.length - 1)
// The thumb's left edge, as a share of the track minus the thumb.
const share = (i: number) => (last.value ? i / last.value : 0)
const atMax = computed(() => last.value > 0 && index.value === last.value)

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
      <span :class="atMax ? 'text-accent' : 'text-strong'">{{ LABEL[model] }}</span>
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
      class="relative h-3.5 cursor-pointer touch-none overflow-hidden rounded-control bg-line outline-none focus-visible:outline focus-visible:outline-offset-2 focus-visible:outline-accent"
      @pointerdown="onDown"
      @keydown="onKey"
    >
      <!-- Fill up to the thumb's right edge. -->
      <div
        class="absolute inset-y-0 left-0 rounded-control bg-accent transition-[width] duration-150"
        :style="{ width: `calc((100% - 14px) * ${share(index)} + 14px)` }"
      />
      <span
        class="absolute inset-y-0 w-3.5 rounded-control bg-strong transition-[left] duration-150"
        :style="{ left: `calc((100% - 14px) * ${share(index)})` }"
      />
    </div>
  </div>
</template>
