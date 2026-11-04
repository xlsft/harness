<script setup lang="ts">
// A row that names another session and opens it: the session's name, title and, while it is known here, its state.
const props = defineProps<{ id: string, name?: string, title?: string, state?: string }>()
const store = useSessionStore()
const route = useRoute()
const live = computed(() => store.sessions[props.id]?.meta)
const label = computed(() => live.value?.title || props.title || props.name || props.id)
const state = computed(() => (live.value?.status ?? props.state ?? '').replaceAll('_', ' '))
async function open() {
  await store.openLinked(props.id)
  if (route.path !== '/' || route.query.new) await navigateTo({ path: '/' })
}
</script>

<template>
  <button
    type="button"
    class="flex w-full min-w-0 items-center gap-2 border border-default px-3 py-1.5 text-left text-[12.5px] hover:bg-hover"
    :data-session-row="id"
    @click="open"
  >
    <HIcon
      name="session"
      :size="14"
      class="shrink-0 text-muted"
    />
    <span class="min-w-0 flex-1 truncate text-strong">{{ label }}</span>
    <span
      v-if="name"
      class="shrink-0 text-muted"
    >{{ name }}</span>
    <span
      v-if="state"
      class="shrink-0 text-muted"
    >{{ state }}</span>
    <HIcon
      name="arrow-right"
      :size="12"
      class="shrink-0 text-muted"
    />
  </button>
</template>
