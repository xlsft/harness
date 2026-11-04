<script setup lang="ts">
// One tool call as a collapsed row that expands into its full card (ToolGroup, ToolCluster).
import type { ToolItem } from '~/lib/session/types'

const props = defineProps<{ tool: ToolItem, sessionId: string, first?: boolean }>()
const now = useNow()
const open = ref(false)
const store = useSessionStore()
const toast = useToast()
// A running command can go on in the background (not one on another machine).
const canBackground = computed(() => props.tool.name === 'Bash' && props.tool.status === 'running' && !store.sessions[props.sessionId]?.meta.remote)
async function toBackground() {
  try {
    await store.transport?.backgroundToolCall(props.sessionId, props.tool.id, 'user')
  } catch (e) {
    toast.add({ title: 'Could not move it to the background', description: e instanceof Error ? e.message : String(e), color: 'error' })
  }
}
</script>

<template>
  <button
    type="button"
    class="flex w-full items-center gap-[9px] rounded-none px-3 py-[7px] text-left text-[12.5px] text-muted hover:bg-hover"
    :class="first ? '' : 'border-t border-default'"
    :aria-expanded="open"
    @click="open = !open"
  >
    <HIcon
      :name="tool.name === 'Task' || tool.name === 'Agent' ? 'agent' : toolIcon(tool)"
      :spin="tool.status === 'running' && tool.name !== 'Task' && tool.name !== 'Agent'"
    />
    {{ tool.name === 'Task' ? 'Agent' : tool.name }} <span
      v-if="tool.name === 'Bash'"
      class="min-w-0 truncate"
    ><span
      v-if="bashLabel(tool).verb"
      :class="tool.status === 'error' ? 'mr-1 text-bad' : 'mr-1'"
    >{{ bashLabel(tool).verb }}</span><span class="text-strong">{{ bashLabel(tool).text }}</span></span><span
      v-else
      class="min-w-0 truncate text-strong"
    >{{ toolLabel(tool) }}</span>
    <span class="flex-1" />
    <HBtn
      v-if="canBackground"
      title="Keep it running as a background task and let the agent go on"
      @click.stop="toBackground"
    >
      Run in background
    </HBtn>
    <span
      v-if="toolMeta(tool, now, true).cls === 'diff'"
      class="shrink-0 text-[10px]"
    >
      <span class="text-ok">{{ toolMeta(tool, now, true).text.split(' ')[0] }}</span> <span class="text-bad">{{ toolMeta(tool, now, true).text.split(' ')[1] }}</span>
    </span>
    <span
      v-else
      class="shrink-0"
      :class="[toolMeta(tool, now, true).cls, toolMeta(tool, now, true).cls ? 'text-[10.5px]' : 'text-[10px]']"
    >{{ toolMeta(tool, now, true).text }}</span>
  </button>
  <div
    v-if="open"
    class="border-t border-default empty:hidden"
    :class="['Edit', 'MultiEdit', 'Write', 'NotebookEdit', 'Task', 'Agent'].includes(tool.name) && 'p-2'"
  >
    <ToolCall
      :tool="tool"
      :session-id="sessionId"
      headless
    />
  </div>
</template>
