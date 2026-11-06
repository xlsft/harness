<script setup lang="ts">
// SendUserFile: the files as rows of one card. A file that can be rendered opens in the side pane; every file can be
// downloaded. Other devices read a file from the object store; one that did not go there (no store on the server and too
// large to travel with the transcript, or a sensitive path) is available only on the session's machine.
import type { ToolItem } from '~/lib/session/types'
import { formatFileSize, sentFilesOf, type SentFile } from '~/lib/session/userTools'

const props = defineProps<{ tool: ToolItem, sessionId: string }>()
const store = useSessionStore()
const toast = useToast()
const { view, download } = useSentFiles()

const files = computed(() => sentFilesOf(props.tool) ?? [])
const caption = computed(() => String(props.tool.result?.caption ?? ''))
const proactive = computed(() => props.tool.result?.status === 'proactive')
const meta = computed(() => store.sessions[props.sessionId]?.meta)
// Only the machine that runs the session has the file itself.
const unavailable = (f: SentFile) => !!meta.value?.remote && !f.data && !f.stored

async function save(f: SentFile) {
  try {
    await download(props.sessionId, f)
  } catch {
    toast.add({ title: tt('Could not download {0}', [f.name]), color: 'error' })
  }
}
</script>

<template>
  <div class="border border-default bg-surface">
    <div
      v-if="caption || proactive"
      class="flex items-baseline gap-2 border-b border-default px-3 py-2 text-[12.5px]"
    >
      <span class="min-w-0 flex-1 text-strong">{{ caption }}</span>
      <span
        v-if="proactive"
        class="shrink-0 text-[11.5px] text-muted"
      >{{ tt('sent without asking') }}</span>
    </div>
    <div
      v-for="f in files"
      :key="f.path"
      class="flex items-center gap-2 px-3 py-[7px] text-[12.5px]"
    >
      <HIcon
        name="file"
        :size="14"
        class="shrink-0 text-muted"
      />
      <span
        class="min-w-0 flex-1 truncate text-strong"
        :title="f.path"
      >{{ f.name }}</span>
      <span class="shrink-0 text-[11.5px] text-muted">
        {{ unavailable(f) ? `on ${meta?.deviceName || 'another machine'} only · ` : '' }}{{ formatFileSize(f.size) }}
      </span>
      <HBtn
        v-if="f.display === 'inline'"
        :disabled="unavailable(f)"
        @click="view(sessionId, f)"
      >
        {{ tt('Open') }}
      </HBtn>
      <HBtn
        :disabled="unavailable(f)"
        @click="save(f)"
      >
        {{ tt('Download') }}
      </HBtn>
    </div>
  </div>
</template>
