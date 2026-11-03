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
    storage-key="harness-layout-2"
  >
    <AppSidebar />
    <!-- The top bar floats over the page (it lines up with the session header) and the page scrolls under it
         to the window's top edge; the page starts below it. -->
    <div class="relative h-svh min-w-0 flex-1">
      <PageHeader class="absolute inset-x-0 top-0 z-10" />
      <ArtifactsPage v-model:view="view" />
    </div>
  </UDashboardGroup>
</template>
