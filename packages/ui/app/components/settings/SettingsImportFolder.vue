<script setup lang="ts">
// The folder an import reads: typed, chosen with the desktop's picker, or one of the recent folders.
import type { RecentFolder } from '~/gen/harness/agent/v1/agent_pb'
import { isDesktop, pickFolder } from '~/lib/desktop'

const props = defineProps<{ busy?: boolean, showRecent?: boolean }>()
const emit = defineEmits<{ look: [path: string] }>()
const path = defineModel<string>({ default: '' })

const store = useSessionStore()
const folders = ref<RecentFolder[]>([])

onMounted(async () => {
  try {
    folders.value = await store.transport!.listRecentFolders()
  } catch {
    // Older client: the path can still be typed.
  }
})

// The model's new value reaches it only after the parent re-renders, so a chosen path is passed on as is.
function look(p = path.value) {
  if (p.trim()) emit('look', p.trim())
}

async function browse() {
  const p = await pickFolder(path.value)
  if (p) {
    path.value = p
    look(p)
  }
}

function pick(p: string) {
  path.value = p
  look(p)
}
</script>

<template>
  <form
    class="mb-2 flex gap-2"
    @submit.prevent="look()"
  >
    <input
      v-model="path"
      :placeholder="tt('Folder path')"
      :aria-label="tt('Folder path')"
      class="h-8 min-w-0 flex-1 border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent"
    >
    <HBtn
      v-if="isDesktop()"
      @click="browse"
    >
      {{ tt('Choose…') }}
    </HBtn>
    <HBtn
      type="submit"
      :disabled="props.busy || !path.trim()"
    >
      {{ tt('Look') }}
    </HBtn>
  </form>
  <div
    v-if="props.showRecent && folders.length"
    class="mb-3 flex flex-wrap gap-1.5"
  >
    <HBtn
      v-for="f in folders.slice(0, 6)"
      :key="f.path"
      :title="f.path"
      @click="pick(f.path)"
    >
      {{ f.name }}
    </HBtn>
  </div>
</template>
