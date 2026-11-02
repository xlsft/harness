<script setup lang="ts">
// Plain element: markdown typography lives in the `md-body` utility (main.css).
defineProps<{ href?: string, target?: string }>()
</script>

<template>
  <a
    :href="href"
    :target="target"
  ><slot /></a>
</template>
