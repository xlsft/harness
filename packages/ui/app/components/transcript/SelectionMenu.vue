<script setup lang="ts">
// A small menu over text picked in the transcript: "Start a side chat" opens a side chat quoting it, "Reply" quotes
// it in the composer (useQuoteRequests). Shows when a selection ends inside `root` (not in fields or code inputs),
// hides when the selection goes away or the transcript scrolls.
const props = defineProps<{ root: HTMLElement | null }>()
const { replyWith, startSideChat } = useQuoteRequests()
const store = useSessionStore()
const sideChats = computed(() => !store.current?.meta.unsupported.includes('side_chat'))

const at = ref<{ x: number, y: number } | null>(null)
const picked = ref('')
const menu = ref<HTMLElement | null>(null)

function update() {
  const sel = window.getSelection()
  const text = sel?.toString().trim() ?? ''
  if (!sel || !text || sel.rangeCount === 0 || !props.root) return hide()
  const range = sel.getRangeAt(0)
  const node = range.commonAncestorContainer
  const el = node instanceof Element ? node : node.parentElement
  if (!el || !props.root.contains(el) || el.closest('input, textarea, [contenteditable="true"]')) return hide()
  const rect = range.getBoundingClientRect()
  if (!rect.width && !rect.height) return hide()
  picked.value = text
  at.value = { x: rect.left + rect.width / 2, y: rect.top }
}
function hide() {
  at.value = null
}

// After the mouse or keyboard finishes a selection; a press elsewhere, a collapsed selection or a scroll hides it.
function onUp() {
  setTimeout(update, 0)
}
function onDown(e: PointerEvent) {
  if (menu.value?.contains(e.target as Node)) return
  hide()
}
function onSelectionChange() {
  if (at.value && !window.getSelection()?.toString().trim()) hide()
}
function onScroll() {
  hide()
}
watch(() => props.root, (el, old) => {
  old?.removeEventListener('scroll', onScroll, true)
  el?.addEventListener('scroll', onScroll, true)
}, { immediate: true })
onMounted(() => {
  document.addEventListener('mouseup', onUp)
  document.addEventListener('keyup', onUp)
  document.addEventListener('pointerdown', onDown, true)
  document.addEventListener('selectionchange', onSelectionChange)
})
onBeforeUnmount(() => {
  document.removeEventListener('mouseup', onUp)
  document.removeEventListener('keyup', onUp)
  document.removeEventListener('pointerdown', onDown, true)
  document.removeEventListener('selectionchange', onSelectionChange)
  props.root?.removeEventListener('scroll', onScroll, true)
})

function act(fn: (t: string) => void) {
  fn(picked.value)
  hide()
  window.getSelection()?.removeAllRanges()
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="at"
      ref="menu"
      role="menu"
      aria-label="Selection"
      class="fixed z-50 flex -translate-x-1/2 -translate-y-[calc(100%+8px)] items-center border border-accented bg-hover text-[12.5px] shadow-none"
      :style="{ left: `${at.x}px`, top: `${at.y}px` }"
    >
      <template v-if="sideChats">
        <button
          type="button"
          role="menuitem"
          class="h-8 rounded-none px-3 text-strong hover:bg-raised"
          @click="act(startSideChat)"
        >
          Start a side chat
        </button>
        <span class="h-4 w-px bg-line" />
      </template>
      <button
        type="button"
        role="menuitem"
        class="h-8 rounded-none px-3 text-strong hover:bg-raised"
        @click="act(replyWith)"
      >
        Reply
      </button>
    </div>
  </Teleport>
</template>
