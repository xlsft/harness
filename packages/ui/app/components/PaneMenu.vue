<script setup lang="ts">
// Popup layer of a pane: the slot holds one or more absolutely positioned `menu-panel`s, placed
// exactly where the artboards draw them (relative to the pane). Closes on Esc and on a pointer
// press outside its panels; the button that toggles it carries `data-menu-anchor` so that press
// is left to its own click handler.
const open = defineModel<boolean>('open', { default: false })

const root = ref<HTMLElement | null>(null)

function onPointer(e: PointerEvent) {
  const target = e.target as Element | null
  if (!target || root.value?.contains(target)) return
  if (target.closest('[data-menu-anchor]')) return
  open.value = false
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}

watch(open, (v) => {
  if (v) {
    document.addEventListener('pointerdown', onPointer, true)
    document.addEventListener('keydown', onKey)
  } else {
    document.removeEventListener('pointerdown', onPointer, true)
    document.removeEventListener('keydown', onKey)
  }
}, { immediate: true })

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onPointer, true)
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div
    v-if="open"
    ref="root"
    class="contents"
  >
    <slot />
  </div>
</template>
