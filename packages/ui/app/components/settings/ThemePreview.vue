<script setup lang="ts">
// A small picture of the app in a theme's colours (sidebar, a message, a reply and the composer), drawn from
// its tokens so every theme in app/themes.ts gets a preview without an image.
import type { Theme } from '~/themes'

const props = defineProps<{ theme: Theme }>()
const c = computed(() => props.theme.tokens)
const r = computed(() => props.theme.radius ?? {})
</script>

<template>
  <div
    class="flex h-[92px] w-full overflow-hidden"
    :style="{ background: c.ground }"
    aria-hidden="true"
  >
    <div
      class="flex w-[30%] flex-col gap-[5px] px-[7px] pt-[9px]"
      :style="{ background: c.surface, borderRight: `1px solid ${c.divider}` }"
    >
      <span
        class="h-[4px] w-[70%]"
        :style="{ background: c.text }"
      />
      <span
        class="h-[4px] w-[85%]"
        :style="{ background: c.raised, borderLeft: `2px solid ${c.accent}` }"
      />
      <span
        class="h-[4px] w-[60%]"
        :style="{ background: c.dimmed }"
      />
      <span
        class="h-[4px] w-[75%]"
        :style="{ background: c.dimmed }"
      />
    </div>
    <div class="flex flex-1 flex-col gap-[6px] px-[9px] pt-[9px]">
      <span
        class="h-[11px] w-[55%] self-end"
        :style="{ background: c.hover, border: `1px solid ${c.line}`, borderRadius: r.chip }"
      />
      <span
        class="h-[4px] w-[90%]"
        :style="{ background: c.text }"
      />
      <span
        class="h-[4px] w-[70%]"
        :style="{ background: c.muted }"
      />
      <span class="flex gap-[3px]">
        <span
          class="h-[4px] w-[18%]"
          :style="{ background: c.ok }"
        />
        <span
          class="h-[4px] w-[12%]"
          :style="{ background: c.warn }"
        />
        <span
          class="h-[4px] w-[10%]"
          :style="{ background: c.pink }"
        />
      </span>
      <span
        class="mt-auto mb-[7px] flex h-[16px] items-center justify-end pr-[2px]"
        :style="{ background: c.surface, border: `1px solid ${c.accent}`, borderRadius: r.chip }"
      >
        <span
          class="size-[10px]"
          :style="{ background: c.accent, borderRadius: r.chip ? '3px' : 0 }"
        />
      </span>
    </div>
  </div>
</template>
