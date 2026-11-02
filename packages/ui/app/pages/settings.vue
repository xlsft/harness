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
    <UDashboardPanel
      id="settings"
      :ui="{ root: 'min-h-0 h-svh min-w-0', body: 'p-0 sm:p-0 gap-0 overflow-y-auto' }"
    >
      <template #body>
        <SettingsPage />
      </template>
    </UDashboardPanel>
  </UDashboardGroup>
</template>
