<script setup lang="ts">
// Every image the session's tools returned, one at a time over the whole window: a caption saying what produced it,
// the arrows and arrow keys walk through them, Esc or a click outside the image closes.
import { imageSrc } from '~/lib/session/objects'
import type { SessionState, ToolItem } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()
const { url, close } = useImageViewer()

const images = computed(() => props.session.items
  .filter((i): i is ToolItem => i.kind === 'tool' && !!i.images?.length)
  .flatMap(t => t.images!.map(img => ({ url: imageSrc(img), caption: toolLabel(t) || t.name }))))
const at = computed(() => images.value.findIndex(i => i.url === url.value))
const current = computed(() => images.value[at.value])

function go(step: number) {
  const next = images.value[at.value + step]
  if (next) url.value = next.url
}
function onKey(e: KeyboardEvent) {
  if (!current.value) return
  if (e.key === 'Escape') close()
  else if (e.key === 'ArrowLeft') go(-1)
  else if (e.key === 'ArrowRight') go(1)
  else return
  e.preventDefault()
  e.stopPropagation()
}
onMounted(() => window.addEventListener('keydown', onKey, true))
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey, true)
  close()
})
</script>

<template>
  <Teleport to="body">
    <div
      v-if="current"
      role="dialog"
      aria-modal="true"
      aria-label="Image"
      class="fixed inset-0 z-50 flex flex-col bg-black/85"
      @click.self="close"
    >
      <div
        class="flex min-h-0 flex-1 items-center justify-center p-10"
        @click.self="close"
      >
        <img
          :src="current.url"
          :alt="current.caption"
          class="max-h-full max-w-full object-contain"
        >
      </div>
      <div class="flex shrink-0 items-center gap-3 px-4 pb-3 text-[12.5px] text-toned">
        <span class="min-w-0 flex-1 truncate">{{ current.caption }}</span>
        <HIconBtn
          icon="arrow-left"
          label="Previous image"
          :size="30"
          :disabled="at <= 0"
          @click="go(-1)"
        />
        <span class="tabular-nums">{{ at + 1 }} of {{ images.length }}</span>
        <HIconBtn
          icon="arrow-right"
          label="Next image"
          :size="30"
          :disabled="at >= images.length - 1"
          @click="go(1)"
        />
      </div>
      <HIconBtn
        icon="close"
        label="Close"
        :size="30"
        class="absolute top-3 right-3"
        @click="close"
      />
    </div>
  </Teleport>
</template>
