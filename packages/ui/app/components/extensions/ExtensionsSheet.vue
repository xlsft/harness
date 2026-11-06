<script setup lang="ts">
// A panel over the right of the page, the whole width when narrow: the detail of an item and the Add forms. Esc and the backdrop close it.
defineProps<{ label: string, footer?: boolean }>()
const emit = defineEmits<{ close: [] }>()

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
const closeBtn = ref<HTMLElement | null>(null)
onMounted(() => {
  document.addEventListener('keydown', onKey)
  closeBtn.value?.focus()
})
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-40 bg-black/50"
      data-sheet-backdrop
      @click="emit('close')"
    />
    <aside
      role="dialog"
      aria-modal="true"
      :aria-label="label"
      class="fixed inset-y-0 right-0 z-50 flex w-[560px] max-w-full flex-col border-l border-default bg-default"
    >
      <header class="flex shrink-0 items-start gap-3 border-b border-default px-5 py-3.5">
        <div class="min-w-0 flex-1">
          <slot name="header" />
        </div>
        <button
          ref="closeBtn"
          type="button"
          :aria-label="tt('Close')"
          class="flex size-7 shrink-0 cursor-pointer items-center justify-center text-muted hover:text-strong"
          @click="emit('close')"
        >
          <HIcon
            name="close"
            :size="14"
          />
        </button>
      </header>
      <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
        <slot />
      </div>
      <footer
        v-if="footer && $slots.actions"
        class="flex shrink-0 flex-wrap items-center gap-2 border-t border-default px-5 py-3"
      >
        <slot name="actions" />
      </footer>
    </aside>
  </Teleport>
</template>
