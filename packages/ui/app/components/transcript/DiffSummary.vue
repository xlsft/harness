<script setup lang="ts">
// Changes in this turn (Diff.dc.html "summary"): one row per file with +/− and a five-square bar.
import type { FileChange } from '~/utils/tools'

// `totalOnly`: just the totals row (the diff pane draws the file rows as separators inside the diff).
const props = withDefaults(defineProps<{ files: FileChange[], flush?: boolean, totalOnly?: boolean }>(), { flush: false, totalOnly: false })
defineEmits<{ open: [path: string] }>()

const total = computed(() => props.files.reduce((a, f) => ({ add: a.add + f.add, del: a.del + f.del }), { add: 0, del: 0 }))

function bars(f: FileChange) {
  const g = Math.round((f.add / Math.max(1, f.add + f.del)) * 5)
  return Array.from({ length: 5 }, (_, i) => i < g ? 'bg-ok' : 'bg-bad')
}
</script>

<template>
  <div
    class="bg-surface"
    :class="flush ? 'border-b border-default' : 'border border-default'"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-[7px] text-[12px]">
      <span class="text-white">{{ files.length }} files changed</span>
      <span class="flex-1" />
      <span class="text-ok">+{{ total.add }}</span><span class="text-bad">−{{ total.del }}</span>
    </div>
    <a
      v-for="f in totalOnly ? [] : files"
      :key="f.path"
      href="#"
      class="flex items-center gap-2.5 border-b border-muted px-3 py-[5px] text-[12px] text-toned last:border-b-0 hover:bg-hover"
      @click.prevent="$emit('open', f.path)"
    >
      <span
        class="w-3"
        :class="f.kind === 'A' ? 'text-ok' : f.kind === 'D' ? 'text-bad' : 'text-warn'"
      >{{ f.kind }}</span>
      <span class="min-w-0 flex-1 truncate">{{ f.path }}</span>
      <span class="text-ok">+{{ f.add }}</span><span class="text-bad">−{{ f.del }}</span>
      <span class="flex gap-0.5"><span
        v-for="(c, i) in bars(f)"
        :key="i"
        class="size-[7px]"
        :class="c"
      /></span>
    </a>
  </div>
</template>
