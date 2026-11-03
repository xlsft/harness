<script setup lang="ts">
// Settings page: sidebar + SettingsPage (?section=general|models|account|about).
useShortcuts()
</script>

<template>
  <UDashboardGroup
    unit="px"
    storage="local"
    storage-key="harness-layout"
  >
    <AppSidebar />
    <!-- The top bar floats over the page (it lines up with the session header) and the page scrolls under it
         to the window's top edge; the page starts below it. -->
    <div class="relative h-svh min-w-0 flex-1">
      <PageHeader class="absolute inset-x-0 top-0 z-10" />
      <SettingsPage />
    </div>
  </UDashboardGroup>
</template>
