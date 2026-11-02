<script setup lang="ts">
// Accent block cursor (terminal prompt, streaming side-chat answers); size comes from the class.
withDefaults(defineProps<{ blink?: boolean }>(), { blink: true })
</script>

<template>
  <span
    class="inline-block bg-accent"
    :class="{ 'h-caret': blink }"
  />
</template>
