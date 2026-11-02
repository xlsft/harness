<script setup lang="ts">
// One tab of a pane's tab strip (terminal, browser, files, side chats): the selected tab sits on
// #0a0a0a with white text and a 2px accent underline; the close mark shows on the selected tab
// (and on hover for the others). `edge` is the side of the hairline separator.
withDefaults(defineProps<{
  selected: boolean
  edge?: 'left' | 'right'
  closable?: boolean
  italic?: boolean
}>(), {
  edge: 'right',
  closable: true,
  italic: false
})

const emit = defineEmits<{ select: [], close: [] }>()
</script>

<template>
  <div
    role="tab"
    tabindex="0"
    :aria-selected="selected"
    class="group flex cursor-default items-center border-b-2 border-line text-[12px] whitespace-nowrap"
    :class="[
      edge === 'left' ? 'border-l' : 'border-r',
      selected ? 'border-b-accent bg-surface text-white' : 'border-b-transparent text-muted hover:text-toned',
      italic && 'italic'
    ]"
    @click="emit('select')"
    @keydown.enter="emit('select')"
  >
    <slot />
    <button
      v-if="closable"
      type="button"
      aria-label="Close tab"
      class="flex shrink-0 items-center justify-center hover:text-white"
      :class="selected ? 'text-muted' : 'text-transparent group-hover:text-muted'"
      @click.stop="emit('close')"
    >
      <HIcon
        name="close"
        :size="11"
      />
    </button>
  </div>
</template>
