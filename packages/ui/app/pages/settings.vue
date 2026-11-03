<script setup lang="ts">
// Old links to the settings page open the settings modal over the session view.
const route = useRoute()
await navigateTo({ path: '/', query: { settings: String(route.query.section || 'appearance') } }, { replace: true })
</script>

<template>
  <div />
</template>
