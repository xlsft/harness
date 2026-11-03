<script setup lang="ts">
// Text button in the design's three looks: hairline outline (default), accent fill, and bare text.
// Sizes follow the artboards: xs = 11px · 1px 8px, sm = 11.5px · 3px 10px, md = 12px · 5px 12px, lg = 12px · 6px 12px.
const props = withDefaults(defineProps<{
  variant?: 'outline' | 'primary' | 'ghost'
  size?: 'xs' | 'sm' | 'md' | 'lg'
  tone?: 'default' | 'danger' | 'muted'
  type?: 'button' | 'submit'
}>(), {
  variant: 'outline',
  size: 'xs',
  tone: 'default',
  type: 'button'
})

const cls = computed(() => [
  'inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap leading-[1.6] select-none disabled:opacity-50',
  {
    xs: 'px-2 py-px text-[11px]',
    sm: 'px-2.5 py-[3px] text-[11.5px]',
    md: 'px-3 py-[5px] text-[12px]',
    lg: 'px-3 py-1.5 text-[12px]'
  }[props.size],
  props.variant === 'primary'
    ? 'bg-accent text-on-accent hover:brightness-110'
    : props.variant === 'outline'
      ? 'border border-line hover:bg-hover'
      : 'hover:text-highlighted',
  props.variant !== 'primary' && (props.tone === 'danger' ? 'text-bad' : props.tone === 'muted' ? 'text-muted' : 'text-strong')
])
</script>

<template>
  <button
    :type="type"
    :class="cls"
  >
    <slot />
  </button>
</template>
