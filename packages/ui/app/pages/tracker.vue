<script setup lang="ts">
// Task tracker: the user's tasks across sessions.
useShortcuts()
</script>

<template>
  <UDashboardGroup
    unit="px"
    storage="local"
    storage-key="harness-layout-2"
  >
    <AppSidebar />
    <div class="relative h-svh min-w-0 flex-1">
      <PageHeader class="absolute inset-x-0 top-0 z-10" />
      <TrackerPage />
    </div>
  </UDashboardGroup>
</template>
