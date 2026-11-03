<script setup lang="ts">
// Square switch from the artboards: on = accent track with a white knob at the end (28×15),
// off = #2b2b2b track with a grey knob (30×16 in the tools menu). `disabled` locks it where it is.
const model = defineModel<boolean>({ default: false })
defineProps<{ muted?: boolean, label?: string, disabled?: boolean }>()
</script>

<template>
  <button
    type="button"
    role="switch"
    :aria-checked="model"
    :aria-label="label"
    :disabled="disabled"
    class="flex shrink-0 p-0.5 outline-none focus-visible:outline focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50"
    :class="[model ? 'h-[15px] w-7 justify-end bg-accent' : 'h-4 w-[30px] justify-start bg-line']"
    @click.stop="model = !model"
  >
    <span :class="model ? 'size-[11px] bg-on-accent' : 'size-3 bg-graphite-400'" />
  </button>
</template>
