<script setup lang="ts">
// The one offer, on opening a folder that has Claude Code files and no .harness: quiet, dismissible, remembered for that
// folder on this device. Those files are not read otherwise. Nothing happens until Import is pressed.
import { comingOver, kindLabel, offerSummary, type ProjectImportScan } from '~/lib/import/project'

const props = defineProps<{ cwd: string }>()

const store = useSessionStore()
const scan = ref<ProjectImportScan | null>(null)
const open = ref(false)
const busy = ref(false)
const done = ref('')
const error = ref('')

const list = computed(() => comingOver(scan.value?.items ?? []))
const needsTrust = computed(() => list.value.some(i => i.needsTrust))

watch(() => props.cwd, async (path) => {
  scan.value = null
  open.value = false
  done.value = ''
  error.value = ''
  if (!path) return
  try {
    const found = await store.transport!.projectImport().scan(path)
    if (props.cwd === path && found.offer) scan.value = found
  } catch {
    // An older client, or a folder that went away: no offer.
  }
}, { immediate: true })

async function dismiss() {
  const path = props.cwd
  scan.value = null
  try {
    await store.transport!.projectImport().dismiss(path)
  } catch {
    // Not remembered; it asks again next time.
  }
}

async function accept() {
  busy.value = true
  error.value = ''
  try {
    const r = await store.transport!.projectImport().run(props.cwd)
    if (r.errors > 0) {
      error.value = `${r.errors} could not be imported. Settings > Import shows which.`
    } else {
      done.value = `Imported ${r.imported} into .harness and AGENTS.md.`
    }
    scan.value = null
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div
    v-if="scan || done || error"
    class="w-full max-w-[440px] border border-default px-3 py-2 text-[12px] text-muted"
    :role="error ? 'alert' : undefined"
  >
    <template v-if="scan">
      <div class="flex items-center gap-2">
        <span class="min-w-0 flex-1">
          {{ tt('This folder has Claude Code files, which aren\'t read.') }}
          <button
            type="button"
            class="inline text-left underline"
            :aria-expanded="open"
            @click="open = !open"
          >{{ offerSummary(scan.items) }}</button>
        </span>
        <HBtn
          size="xs"
          :disabled="busy"
          @click="accept"
        >
          {{ tt('Import') }}
        </HBtn>
        <HIconBtn
          icon="close"
          :label="tt('Dismiss')"
          :disabled="busy"
          @click="dismiss"
        />
      </div>
      <ul
        v-if="open"
        class="m-0 mt-2 list-none space-y-0.5 p-0"
      >
        <li
          v-for="i in list"
          :key="i.source"
          class="truncate"
          :title="i.note || undefined"
        >
          {{ kindLabel(i.kind) }}: {{ i.source }} → {{ i.target }}
        </li>
      </ul>
      <p
        v-if="open && needsTrust"
        class="m-0 mt-2"
      >
        {{ tt('This includes hooks or allow rules. They apply only once you trust this folder.') }}
      </p>
    </template>
    <template v-else>
      {{ error || done }}
    </template>
  </div>
</template>
