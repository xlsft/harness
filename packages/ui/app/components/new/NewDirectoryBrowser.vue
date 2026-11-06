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
    const msg = err instanceof Error ? err.message : String(err)
    error.value = /unimplemented/i.test(msg)
      ? 'This harness-client cannot browse folders yet. Update it, or type a path above.'
      : msg
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
        :aria-label="tt('Path')"
        class="min-w-0 flex-1 bg-transparent text-strong outline-none"
      >
      <HKeys keys="enter" />
    </form>
    <div class="h-[320px] overflow-y-auto py-1">
      <div
        v-if="listing?.parent"
        class="flex cursor-pointer items-center gap-2 px-2.5 py-[5px] text-muted hover:bg-raised hover:text-strong"
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
        class="flex cursor-pointer items-center gap-2 px-2.5 py-[5px] text-toned hover:bg-raised hover:text-strong"
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
        {{ tt('No subfolders') }}
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
        class="h-7 border border-default px-2.5 text-toned hover:text-strong"
        @click="emit('close')"
      >
        {{ tt('Cancel') }}
      </button>
      <button
        type="button"
        :disabled="(!listing && !typed.trim()) || loading"
        class="h-7 bg-accent px-2.5 text-on-accent disabled:opacity-50"
        @click="emit('pick', listing?.path ?? typed.trim())"
      >
        {{ tt('Use this folder') }}
      </button>
    </div>
  </div>
</template>
