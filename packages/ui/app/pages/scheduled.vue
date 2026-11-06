<script setup lang="ts">
// Scheduled: tasks that run by themselves on this machine or another of the user's.
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
      <ScheduledPage />
    </div>
  </UDashboardGroup>
</template>
