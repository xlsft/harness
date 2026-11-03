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
    <!-- Laid out like the session page (header straight in the column), so the top bar lines up with it. -->
    <div class="flex h-svh min-w-0 flex-1 flex-col">
      <PageHeader />
      <div class="min-h-0 flex-1">
        <SettingsPage />
      </div>
    </div>
  </UDashboardGroup>
</template>
