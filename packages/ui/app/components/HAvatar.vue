<script setup lang="ts">
// A person's avatar: their picture, else the first letter of their name on a colour of their own. The colour comes from a
// hash of `seed` (the username, else the email or id), so the same person has the same colour everywhere and two people
// next to each other rarely share one (utils/avatarColor.ts).
const props = withDefaults(defineProps<{ src?: string, name?: string, seed?: string, size?: number }>(), { src: '', name: '', seed: '', size: 22 })

const initial = computed(() => (props.name || props.seed || '?').trim().charAt(0).toUpperCase() || '?')
const background = computed(() => avatarColor(props.seed || props.name))
const box = computed(() => ({ width: `${props.size}px`, height: `${props.size}px` }))
const failed = ref(false)
watch(() => props.src, () => (failed.value = false))
</script>

<template>
  <img
    v-if="src && !failed"
    :src="src"
    alt=""
    :style="box"
    class="shrink-0 rounded-chip object-cover"
    @error="failed = true"
  >
  <span
    v-else
    aria-hidden="true"
    :style="{ ...box, background, fontSize: `${Math.round(size * 0.46)}px` }"
    class="flex shrink-0 items-center justify-center rounded-chip font-bold text-white select-none"
  >{{ initial }}</span>
</template>
