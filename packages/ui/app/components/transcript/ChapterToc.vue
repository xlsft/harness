<script setup lang="ts">
// The floating table of contents of a transcript: its chapters (MarkChapter); a click scrolls to the divider.
import type { Chapter } from '~/lib/session/links'

const props = defineProps<{ chapters: Chapter[], root: HTMLElement | null }>()
const open = ref(false)
// A divider may be above what the transcript has mounted so far.
const showAll = useState<boolean>('transcript-show-all', () => false)
async function jump(id: string) {
  showAll.value = true
  await nextTick()
  props.root?.querySelector(`[data-chapter="${CSS.escape(id)}"]`)?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  open.value = false
}
</script>

<template>
  <div class="absolute top-3 right-3 z-10 flex flex-col items-end gap-1">
    <HIconBtn
      icon="list"
      :label="tt('Chapters')"
      :size="28"
      :icon-size="14"
      :aria-expanded="open"
      @click="open = !open"
    />
    <nav
      v-if="open"
      :aria-label="tt('Chapters')"
      class="flex max-h-[60vh] w-64 flex-col overflow-y-auto border border-default bg-surface py-1"
    >
      <button
        v-for="c in chapters"
        :key="c.id"
        type="button"
        class="truncate px-3 py-1.5 text-left text-[12.5px] text-toned hover:bg-hover hover:text-strong"
        :title="c.summary || c.title"
        @click="jump(c.id)"
      >
        {{ c.title }}
      </button>
    </nav>
  </div>
</template>
