<script setup lang="ts">
// A titled group of rows, with its count. An empty group isn't rendered: callers send only what has rows.
defineProps<{ title: string, count: number }>()
</script>

<template>
  <section class="flex flex-col">
    <h2 class="m-0 flex items-center gap-2 pb-1.5 text-[13px] font-medium text-strong">
      {{ title }}
      <span class="bg-raised px-1.5 text-[11px] font-normal text-muted">{{ count }}</span>
    </h2>
    <slot />
  </section>
</template>
