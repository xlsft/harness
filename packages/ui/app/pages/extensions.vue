<script setup lang="ts">
// Extensions: skills, connectors and plugins, what this device has and what it could add.
useShortcuts()
</script>

<template>
  <UDashboardGroup
    unit="px"
    storage="local"
    storage-key="harness-layout-2"
  >
    <AppSidebar />
    <div class="flex h-svh min-w-0 flex-1 flex-col">
      <PageHeader />
      <ExtensionsPage class="min-h-0 flex-1" />
    </div>
  </UDashboardGroup>
</template>
