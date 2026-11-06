<script setup lang="ts">
// The live regions of screen reader mode, and a way to jump past the sidebar to the message box.
const { enabled, polite, assertive } = useScreenReader()

function toComposer() {
  document.querySelector<HTMLElement>('textarea, [contenteditable="true"]')?.focus()
}
</script>

<template>
  <div
    aria-live="polite"
    aria-atomic="true"
    class="sr-only"
  >
    {{ polite }}
  </div>
  <div
    aria-live="assertive"
    aria-atomic="true"
    class="sr-only"
  >
    {{ assertive }}
  </div>
  <button
    v-if="enabled"
    type="button"
    class="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:bg-raised focus:p-2 focus:text-strong"
    @click="toComposer"
  >
    Skip to the message box
  </button>
</template>
