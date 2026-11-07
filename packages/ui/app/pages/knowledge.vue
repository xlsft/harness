<script setup lang="ts">
// Knowledge: documents on the server that agents search.
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
      <KnowledgePage />
    </div>
  </UDashboardGroup>
</template>
