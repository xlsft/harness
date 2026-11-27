<script setup lang="ts">
// Square switch, 28×16 in both states: on = accent track with a white knob at the end, off = #2b2b2b track with a grey
// knob at the start. `disabled` locks it where it is. The knob slides and the track changes colour over 150ms; with
// reduced motion it switches at once.
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
    class="flex shrink-0 justify-start p-0.5 h-4 w-7 transition-colors duration-150 ease-out outline-none focus-visible:outline focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50 motion-reduce:transition-none"
    :class="model ? 'bg-accent' : 'bg-line'"
    @click.stop="model = !model"
  >
    <span
      class="rounded-[max(0px,calc(var(--radius-control)-2px))] size-3 transition-[translate,background-color] duration-150 ease-out motion-reduce:transition-none"
      :class="model ? 'translate-x-3 bg-on-accent' : 'translate-x-0 bg-graphite-400'"
    />
  </button>
</template>
