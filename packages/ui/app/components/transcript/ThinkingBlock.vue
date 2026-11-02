<script setup lang="ts">
// Thinking (Event.dc.html "thinking"): "Thought for 12s" toggle and the italic thought, expanded by default.
const props = withDefaults(defineProps<{ text: string, durationMs?: number, streaming?: boolean }>(), { durationMs: 0, streaming: false })
const open = ref(true)
const label = computed(() => props.streaming ? 'Thinking' : props.durationMs ? `Thought for ${Math.max(1, Math.round(props.durationMs / 1000))}s` : 'Thought')
</script>

<template>
  <div class="flex flex-col gap-1.5">
    <button
      type="button"
      class="flex items-center gap-2 text-[12.5px] text-muted hover:text-toned"
      :aria-expanded="open"
      @click="open = !open"
    >
      <HIcon name="thinking" />
      {{ label }}
      <HIcon
        name="down"
        :size="13"
        :class="open ? '' : '-rotate-90'"
      />
    </button>
    <div
      v-if="open"
      class="border-l-2 border-default py-0.5 pl-3.5 text-[12.5px] leading-[1.6] text-muted italic"
    >
      {{ text }}
    </div>
  </div>
</template>
