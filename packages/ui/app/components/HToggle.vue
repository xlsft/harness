<script setup lang="ts">
// Square switch from the artboards: on = accent track with a white knob at the end (28×15),
// off = #2b2b2b track with a grey knob (30×16 in the tools menu).
const model = defineModel<boolean>({ default: false })
defineProps<{ muted?: boolean, label?: string }>()
</script>

<template>
  <button
    type="button"
    role="switch"
    :aria-checked="model"
    :aria-label="label"
    class="flex shrink-0 p-0.5 outline-none focus-visible:outline focus-visible:outline-accent"
    :class="[model ? 'h-[15px] w-7 justify-end bg-accent' : 'h-4 w-[30px] justify-start bg-line']"
    @click.stop="model = !model"
  >
    <span :class="model ? 'size-[11px] bg-white' : 'size-3 bg-graphite-400'" />
  </button>
</template>
