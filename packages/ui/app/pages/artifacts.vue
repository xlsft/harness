<script setup lang="ts">
// Artifacts page (MainArtifactsGrid / MainArtifactsList): sidebar + grid or list (?view=list).
const route = useRoute()
const router = useRouter()
const view = computed<'grid' | 'list'>({
  get: () => route.query.view === 'list' ? 'list' : 'grid',
  set: v => router.replace({ query: { ...route.query, view: v === 'grid' ? undefined : v } })
})
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
      id="artifacts"
      :ui="{ root: 'min-h-0 h-svh min-w-0', body: 'p-0 sm:p-0 gap-0 overflow-y-auto' }"
    >
      <template #body>
        <ArtifactsPage v-model:view="view" />
      </template>
    </UDashboardPanel>
  </UDashboardGroup>
</template>
