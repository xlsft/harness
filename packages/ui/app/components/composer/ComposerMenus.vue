<script setup lang="ts">
// Listboxes above the composer (Composer.dc.html "slash" and "mention"): ↑↓ move, ↵ picks, esc closes.
import type { MenuOption } from '~/utils/composer'

defineProps<{ kind: 'slash' | 'mention', options: MenuOption[], active: number }>()
defineEmits<{ pick: [option: MenuOption], hover: [index: number] }>()
</script>

<template>
  <div
    role="listbox"
    :aria-label="kind === 'slash' ? 'Commands' : 'Mentions'"
    class="border border-accented bg-hover text-[12.5px]"
  >
    <div
      v-for="(o, i) in options"
      :key="o.key"
      role="option"
      :aria-selected="i === active"
      class="flex cursor-default items-center border-l-2 px-2.5 py-1.5"
      :class="[i === active ? 'border-accent bg-raised' : 'border-transparent', kind === 'slash' ? 'gap-3' : 'gap-2.5']"
      @mouseenter="$emit('hover', i)"
      @mousedown.prevent="$emit('pick', o)"
    >
      <template v-if="kind === 'slash'">
        <span class="w-[140px] shrink-0 text-white">{{ o.label }}</span>
        <span class="min-w-0 flex-1 truncate text-muted">{{ o.detail }}</span>
        <span class="text-[11px] text-muted">{{ o.tag }}</span>
      </template>
      <template v-else>
        <HIcon
          :name="o.icon ?? 'file'"
          :size="14"
          :class="o.iconCls"
        />
        <span class="text-white">{{ o.label }}</span>
        <span class="truncate text-muted">{{ o.detail }}</span>
      </template>
    </div>
    <div
      v-if="kind === 'slash'"
      class="flex gap-3.5 border-t border-default px-2.5 py-[5px] text-[11px] text-muted"
    >
      <span>↑↓ navigate</span><span>↵ select</span><span>esc close</span>
    </div>
    <div
      v-if="!options.length"
      class="px-2.5 py-1.5 text-muted"
    >
      No matches
    </div>
  </div>
</template>
