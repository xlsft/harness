<script setup lang="ts">
useSeoMeta({
  title: 'harness',
  description: 'xlsft`s harness: agent sessions, tools and artifacts.'
})

const store = useSessionStore()
await store.init()
</script>

<template>
  <UApp :tooltip="{ delayDuration: 500 }">
    <NuxtPage />
  </UApp>
</template>
