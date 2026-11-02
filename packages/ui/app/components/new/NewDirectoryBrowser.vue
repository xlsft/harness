<script setup lang="ts">
// "Open folder…": walks the client machine's directories (AgentService.ListDirectory). Clicking a folder
// enters it; "Use this folder" picks the one shown. A path can also be typed.
import type { ListDirectoryResponse } from '~/gen/harness/agent/v1/agent_pb'

const props = defineProps<{ start: string }>()
const emit = defineEmits<{ pick: [path: string], close: [] }>()
const store = useSessionStore()

const listing = ref<ListDirectoryResponse | null>(null)
const typed = ref('')
const error = ref('')
const loading = ref(false)

async function go(path: string) {
  loading.value = true
  error.value = ''
  try {
    listing.value = await store.transport!.listDirectory(path)
    typed.value = listing.value.path
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => go(props.start))

const join = (dir: string, name: string) => dir === '/' ? `/${name}` : `${dir}/${name}`
</script>

<template>
  <div class="flex w-[520px] max-w-[calc(100vw-32px)] flex-col border border-accented bg-hover text-[12.5px]">
    <form
      class="flex items-center gap-2 border-b border-default px-2.5 py-1.5"
      @submit.prevent="go(typed)"
    >
      <HIcon
        name="folder"
        :size="13"
        class="text-muted"
      />
      <input
        v-model="typed"
        spellcheck="false"
        aria-label="Path"
        class="min-w-0 flex-1 bg-transparent text-white outline-none"
      >
      <HKeys keys="enter" />
    </form>
    <div class="h-[320px] overflow-y-auto py-1">
      <div
        v-if="listing?.parent"
        class="flex cursor-default items-center gap-2 px-2.5 py-[5px] text-muted hover:bg-raised hover:text-white"
        @click="go(listing.parent)"
      >
        <HIcon
          name="arrow-left"
          :size="13"
        />..
      </div>
      <div
        v-for="e in listing?.entries ?? []"
        :key="e.name"
        class="flex cursor-default items-center gap-2 px-2.5 py-[5px] text-toned hover:bg-raised hover:text-white"
        @click="go(join(listing!.path, e.name))"
      >
        <HIcon
          :name="e.gitRepository ? 'branch' : 'folder'"
          :size="13"
          :class="e.gitRepository ? 'text-accent' : 'text-muted'"
        />
        <span class="min-w-0 flex-1 truncate">{{ e.name }}</span>
      </div>
      <div
        v-if="listing && !listing.entries.length"
        class="px-2.5 py-1.5 text-muted"
      >
        No subfolders
      </div>
      <div
        v-if="error"
        class="mx-2.5 my-1.5 border border-bad px-2.5 py-1.5 text-bad"
      >
        {{ error }}
      </div>
    </div>
    <div class="flex items-center gap-2 border-t border-default px-2.5 py-2">
      <span class="min-w-0 flex-1 truncate text-[11.5px] text-muted">{{ listing?.path }}</span>
      <button
        type="button"
        class="h-7 border border-default px-2.5 text-toned hover:text-white"
        @click="emit('close')"
      >
        Cancel
      </button>
      <button
        type="button"
        :disabled="!listing || loading"
        class="h-7 bg-accent px-2.5 text-white disabled:opacity-50"
        @click="listing && emit('pick', listing.path)"
      >
        Use this folder
      </button>
    </div>
  </div>
</template>
