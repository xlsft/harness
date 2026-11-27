<script setup lang="ts">
// A group's name as a small chip on a colour of its own, from a hash of the name (utils/avatarColor.ts), so a group has
// the same colour wherever it is shown.
const props = defineProps<{ name: string }>()
const background = computed(() => avatarColor(`group:${props.name}`))
</script>

<template>
  <span
    class="inline-flex max-w-full items-center truncate rounded-chip px-1.5 text-[10.5px] leading-[16px] text-white"
    :style="{ background }"
    :title="name"
  >{{ name }}</span>
</template>
