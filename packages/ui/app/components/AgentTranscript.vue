<script setup lang="ts">
// A subagent's transcript in the Agents pane: its messages, and each tool call with its result.
import { SubagentTranscriptItem_Kind, type SubagentTranscriptItem } from '~/gen/harness/agent/v1/subagents_pb'

defineProps<{ items: SubagentTranscriptItem[], error: string }>()
</script>

<template>
  <div class="flex max-h-[420px] flex-col gap-2 overflow-y-auto border-t border-muted px-2.5 py-2 text-[12px]">
    <span
      v-if="error"
      class="text-bad"
    >{{ error }}</span>
    <span
      v-else-if="!items.length"
      class="text-muted"
    >{{ tt('Nothing yet') }}</span>
    <template
      v-for="(it, i) in items"
      :key="i"
    >
      <details
        v-if="it.kind === SubagentTranscriptItem_Kind.TOOL"
        class="text-muted"
      >
        <summary
          class="cursor-pointer truncate"
          :class="it.isError ? 'text-bad' : ''"
        >
          {{ it.toolName }} <span class="text-toned">{{ it.target }}</span>
        </summary>
        <pre class="mt-1 max-h-[200px] overflow-auto border border-line p-2 text-[11px] whitespace-pre-wrap">{{ it.output }}</pre>
      </details>
      <div
        v-else
        class="border-l-2 pl-2"
        :class="it.kind === SubagentTranscriptItem_Kind.USER ? 'border-line text-muted' : 'border-accent'"
      >
        <MarkdownBlock :text="it.text" />
      </div>
    </template>
  </div>
</template>
