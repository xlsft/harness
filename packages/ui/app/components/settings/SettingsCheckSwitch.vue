<script setup lang="ts">
// Small check-box switch with its text, as on the provider cards ("default for everyone", "use for me").
defineProps<{ checked: boolean, label: string, disabled?: boolean }>()
defineEmits<{ toggle: [] }>()
</script>

<template>
  <button
    type="button"
    role="switch"
    :aria-checked="checked"
    :aria-label="label"
    :disabled="disabled"
    class="flex items-center gap-1.5 disabled:cursor-default"
    :class="checked ? 'text-white' : 'text-muted hover:text-white disabled:hover:text-muted'"
    @click="$emit('toggle')"
  >
    <span
      class="flex size-3.5 items-center justify-center border"
      :class="checked ? 'border-accent bg-accent' : 'border-accented'"
    ><HIcon
      v-if="checked"
      name="check"
      :size="10"
      class="text-white"
    /></span>
    <slot />
  </button>
</template>
