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
    <!-- Laid out like the session page (header straight in the column), so the top bar lines up with it. -->
    <div class="flex h-svh min-w-0 flex-1 flex-col">
      <PageHeader />
      <div class="min-h-0 flex-1">
        <ArtifactsPage v-model:view="view" />
      </div>
    </div>
  </UDashboardGroup>
</template>
