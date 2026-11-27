<script setup lang="ts">
// Square switch from the artboards: on = accent track with a white knob at the end (28×15),
// off = #2b2b2b track with a grey knob (30×16 in the tools menu). `disabled` locks it where it is. The knob slides and the
// track changes colour over 150ms; with reduced motion it switches at once.
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
    class="flex shrink-0 justify-start p-0.5 transition-[background-color,width,height] duration-150 ease-out outline-none focus-visible:outline focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50 motion-reduce:transition-none"
    :class="[model ? 'h-[15px] w-7 bg-accent' : 'h-4 w-[30px] bg-line']"
    @click.stop="model = !model"
  >
    <span
      class="rounded-[max(0px,calc(var(--radius-control)-2px))] transition-[transform,width,height,background-color] duration-150 ease-out motion-reduce:transition-none"
      :class="model ? 'size-[11px] translate-x-[13px] bg-on-accent' : 'size-3 translate-x-0 bg-graphite-400'"
    />
  </button>
</template>
