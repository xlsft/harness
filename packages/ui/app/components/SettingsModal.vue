<script setup lang="ts">
// Settings as a modal over the current page (useSettingsModal: ?settings=<section>). Esc, the close button or a
// press on the backdrop closes it.
const modal = useSettingsModal()

function onKey(e: KeyboardEvent) {
  // A popup inside (a select, a menu) closes first on its own Esc.
  if (e.key === 'Escape' && !document.querySelector('[data-reka-popper-content-wrapper]')) modal.close()
}
watch(modal.isOpen, (v) => {
  if (v) document.addEventListener('keydown', onKey)
  else document.removeEventListener('keydown', onKey)
}, { immediate: true })
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <div
      v-if="modal.isOpen.value"
      class="fixed inset-0 z-40 flex items-center justify-center bg-ground/60 p-6 max-sm:p-0"
      @pointerdown.self="modal.close()"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Settings"
        class="flex h-[min(860px,100%)] w-[min(1120px,100%)] flex-col border border-line-strong bg-default"
      >
        <div class="flex h-12 shrink-0 items-center border-b border-line pr-2 pl-5">
          <h1 class="m-0 flex-1 text-[14px] font-semibold text-strong">
            Settings
          </h1>
          <HIconBtn
            icon="close"
            label="Close"
            kbd="esc"
            @click="modal.close()"
          />
        </div>
        <SettingsPage />
      </div>
    </div>
  </Teleport>
</template>
