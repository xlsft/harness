<script setup lang="ts">
// Square text icon of a provider preset ("OR", "AN"): no third-party logos, nothing fetched.
defineProps<{ text: string, dim?: boolean }>()
</script>

<template>
  <span
    aria-hidden="true"
    class="flex size-7 shrink-0 items-center justify-center border border-line bg-raised font-mono text-[10.5px] font-semibold tracking-wide"
    :class="dim ? 'text-dimmed' : 'text-white'"
  >{{ text.slice(0, 2) }}</span>
</template>
