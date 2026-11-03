<script setup lang="ts">
// The animated cat (assets/mascot-cat-animated.svg) in the theme's accent. It sits in a shadow root: the file's own
// rules (path{…}, .p, @keyframes f0…) stay inside, while the --primary and --secondary set here pass through.
import cat from '~/assets/mascot-cat-animated.svg?raw'

withDefaults(defineProps<{ width?: number }>(), { width: 90 })
const host = ref<HTMLElement | null>(null)
onMounted(() => {
  if (!host.value || host.value.shadowRoot) return
  const root = host.value.attachShadow({ mode: 'open' })
  root.innerHTML = `<style>svg{display:block;width:100%;height:auto}</style>${cat}`
})
</script>

<template>
  <span
    ref="host"
    aria-hidden="true"
    class="pointer-events-none block"
    :style="{ 'width': `${width}px`, 'height': `${(width * 17) / 30}px`, '--primary': 'var(--color-accent)', '--secondary': 'var(--color-muted)' }"
  />
</template>
