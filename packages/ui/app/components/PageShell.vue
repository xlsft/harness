<script setup lang="ts">
// The frame of the top-level pages (Artifacts, Scheduled, Knowledge, Issues, Extensions): one width, one title, the
// same top space under the floating page header. Actions sit right of the title.
withDefaults(defineProps<{ title: string, scroll?: boolean }>(), { scroll: true })
</script>

<template>
  <section
    :aria-label="title"
    class="h-full bg-default"
    :class="scroll ? 'overflow-y-auto' : 'flex flex-col overflow-hidden'"
  >
    <div
      class="mx-auto flex w-full max-w-[1080px] flex-col gap-[22px] px-10 pt-[52px] max-sm:px-4"
      :class="scroll ? 'pb-10' : 'min-h-0 flex-1 pb-4'"
    >
      <div class="flex flex-wrap items-center gap-3">
        <h1 class="m-0 flex-1 font-display text-[26px] font-bold text-strong">
          {{ title }}
        </h1>
        <slot name="actions" />
      </div>
      <slot />
    </div>
  </section>
</template>
