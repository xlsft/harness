<script setup lang="ts">
// A card of Discover: icon, name, two lines of description, who publishes it and a + or a check. Numbers and marks appear only when the
// source gives them.
import type { DiscoverItem } from '~/lib/extensions/types'
import { ExtensionKind } from '~/lib/extensions/types'
import { auditSummary, installsLabel, TONE_CLASS } from '~/lib/extensions/view'
import type { IconName } from '~/utils/icons'

const props = defineProps<{ item: DiscoverItem }>()
const emit = defineEmits<{ open: [], add: [] }>()

const icon = computed<IconName>(() => props.item.kind === ExtensionKind.CONNECTOR ? (props.item.options[0]?.remote === false ? 'term' : 'globe') : props.item.kind === ExtensionKind.SKILL ? 'page' : 'box')
const audit = computed(() => auditSummary(props.item.audits))
const installs = computed(() => installsLabel(props.item.installs))
const blocked = computed(() => !!props.item.refusal)
</script>

<template>
  <article
    class="flex min-h-[92px] cursor-pointer gap-3 border border-default p-3 hover:bg-hover"
    data-discover-card
    @click="emit('open')"
  >
    <span class="flex size-9 shrink-0 items-center justify-center border border-default bg-raised text-muted">
      <HIcon
        :name="icon"
        :size="16"
      />
    </span>
    <div class="flex min-w-0 flex-1 flex-col gap-0.5">
      <span class="truncate text-[12.5px] text-strong">{{ item.name }}</span>
      <span class="line-clamp-2 text-[12px] leading-[1.45] text-muted">{{ item.refusal || item.description || item.problem }}</span>
      <span class="mt-auto flex flex-wrap items-center gap-x-2 pt-1 text-[11.5px] text-muted">
        <span v-if="item.publisher">by {{ item.publisher }}</span>
        <span v-if="installs">{{ installs }}</span>
        <span
          v-if="audit"
          :class="TONE_CLASS[audit.tone]"
        >{{ audit.label }}</span>
      </span>
    </div>
    <span
      v-if="item.installed"
      class="flex size-7 shrink-0 items-center justify-center border border-default text-ok"
      :aria-label="`${item.name} is added`"
      role="img"
    >
      <HIcon
        name="check"
        :size="14"
      />
    </span>
    <button
      v-else
      type="button"
      class="flex size-7 shrink-0 items-center justify-center border border-default text-strong hover:bg-raised disabled:cursor-not-allowed disabled:text-disabled"
      :class="!blocked && 'cursor-pointer'"
      :aria-label="`Add ${item.name}`"
      :disabled="blocked"
      @click.stop="emit('add')"
    >
      <HIcon
        name="plus"
        :size="14"
      />
    </button>
  </article>
</template>
