<script setup lang="ts">
// Diff pane (header "Diff" button). Not drawn as its own artboard: built from Diff.dc.html —
// the turn summary on top, then every edit of the session as a unified / split diff.
import type { SessionState, ToolItem } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()
const { closePane } = usePanes()

const edits = computed(() => props.session.items.filter((i): i is ToolItem => i.kind === 'tool' && ['Edit', 'MultiEdit', 'Write'].includes(i.name) && i.status === 'ok' && patchOf(i).length > 0))
const files = computed(() => fileChanges(edits.value))

function scrollTo(path: string) {
  document.getElementById(`diff-${path}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
</script>

<template>
  <section
    aria-label="Diff"
    class="flex h-full flex-col bg-default"
  >
    <div class="flex h-11 shrink-0 items-center gap-0.5 border-b border-default pr-1.5 pl-3.5">
      <span class="font-display text-[13px] text-white">Diff</span>
      <span class="flex-1" />
      <HIconBtn
        icon="full"
        label="Maximize"
        :size="30"
      />
      <HIconBtn
        icon="close"
        label="Close"
        :size="30"
        @click="closePane('diff')"
      />
    </div>
    <div
      v-if="!edits.length"
      class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
    >
      <HIcon
        name="diff"
        :size="24"
        class="text-muted"
      />
      <span class="text-white">No changes yet</span>
      <span class="max-w-80 text-[12px] leading-[1.55] text-muted">Edits the agent makes in this session show up here for review.</span>
    </div>
    <div
      v-else
      class="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-3"
    >
      <DiffSummary
        :files="files"
        @open="scrollTo"
      />
      <div
        v-for="t in edits"
        :id="`diff-${String(t.input.file_path)}`"
        :key="t.id"
      >
        <DiffView
          :path="toolTarget(t)"
          :hunks="patchOf(t)"
          :verb="t.name === 'Write' ? 'Write' : 'Edit'"
        />
      </div>
    </div>
  </section>
</template>
