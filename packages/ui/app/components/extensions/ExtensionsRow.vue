<script setup lang="ts">
// One row of Yours: icon, name with tag badges, source and description on one line, a date and a "..." menu. Opening it opens the detail.
import type { RowMenuItem } from '~/components/RowMenu.vue'
import { compactTags, TONE_CLASS, type StatusView } from '~/lib/extensions/view'
import type { IconName } from '~/utils/icons'

const props = defineProps<{
  icon: IconName
  name: string
  tags?: string[]
  source?: string
  description?: string
  date?: string
  status?: StatusView | null
  dim?: boolean
  items: (RowMenuItem | null)[]
}>()
const emit = defineEmits<{ open: [] }>()
const shortTags = computed(() => compactTags(props.tags ?? []))
</script>

<template>
  <div
    class="group flex items-center gap-3 border-b border-default py-2 hover:bg-hover"
    data-extension-row
  >
    <button
      type="button"
      class="flex min-w-0 flex-1 cursor-pointer items-center gap-3 text-left"
      :class="dim && 'opacity-60'"
      @click="emit('open')"
    >
      <span class="flex size-8 shrink-0 items-center justify-center border border-default bg-raised text-muted">
        <HIcon
          :name="icon"
          :size="15"
        />
      </span>
      <span class="flex min-w-0 flex-1 flex-col gap-0.5">
        <span class="flex min-w-0 items-center gap-1.5">
          <span class="truncate text-[12.5px] text-strong">{{ name }}</span>
          <template v-if="shortTags">
            <span class="shrink-0 border border-default px-1 text-[10px] leading-[14px] text-muted">{{ shortTags.first }}</span>
            <span
              v-if="shortTags.more"
              class="shrink-0 border border-default px-1 text-[10px] leading-[14px] text-muted"
            >+{{ shortTags.more }}</span>
          </template>
          <span
            v-if="status"
            class="shrink-0 text-[11px]"
            :class="TONE_CLASS[status.tone]"
          >{{ status.label }}</span>
        </span>
        <span class="truncate text-[12px] text-muted">
          <template v-if="source">{{ source }}<template v-if="description"> · </template></template>{{ description }}
        </span>
      </span>
    </button>
    <span class="w-[64px] shrink-0 text-right text-[11.5px] text-muted max-sm:hidden">{{ date }}</span>
    <RowMenu
      :items="items"
      :label="`Actions for ${name}`"
      align="end"
    >
      <button
        type="button"
        :aria-label="`Actions for ${name}`"
        class="flex size-7 shrink-0 cursor-pointer items-center justify-center text-muted hover:text-strong"
      >
        <HIcon
          name="more"
          :size="15"
        />
      </button>
    </RowMenu>
  </div>
</template>
