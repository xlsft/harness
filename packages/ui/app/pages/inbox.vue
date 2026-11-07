<script setup lang="ts">
// Inbox: every session and background agent of the user, on every machine, by what it needs.
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
      <InboxPage />
    </div>
  </UDashboardGroup>
</template>
