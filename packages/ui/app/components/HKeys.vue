<script setup lang="ts">
// A shortcut as Nuxt UI Kbd keys: "meta+enter" -> [⌘][↵]. Each part is a UKbd value, so special keys
// (meta, ctrl, shift, alt, enter, tab, escape, backspace, arrowup, ...) render per platform (⌘ on macOS,
// Ctrl elsewhere). Several shortcuts can share one hint: "arrowup arrowdown" -> [↑][↓].
const props = withDefaults(defineProps<{
  keys: string
  size?: 'sm' | 'md'
}>(), { size: 'sm' })

const parts = computed(() => props.keys.split(/[+\s]/).filter(Boolean))
</script>

<template>
  <span class="inline-flex shrink-0 items-center gap-0.5 align-middle">
    <UKbd
      v-for="(k, i) in parts"
      :key="i"
      :value="k"
      :size="size"
    />
  </span>
</template>
