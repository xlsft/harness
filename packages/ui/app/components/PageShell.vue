<script setup lang="ts">
// The frame of the top-level pages (Artifacts, Scheduled, Knowledge, Issues, Extensions): one width, one title, the
// same top space under the floating page header. The title stands alone; the first row under it holds the page's
// tabs on the left and its buttons on the right.
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
      <h1 class="m-0 font-display text-[26px] font-bold text-strong">
        {{ title }}
      </h1>
      <slot />
    </div>
  </section>
</template>
