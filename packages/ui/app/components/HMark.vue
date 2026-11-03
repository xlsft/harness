<script setup lang="ts">
// The harness mark, assembling once and then flashing along its rails (docs/assets/logo-animated.svg).
withDefaults(defineProps<{ width?: number }>(), { width: 72 })
</script>

<template>
  <svg
    :width="width"
    :height="Math.round(width * 155 / 232)"
    viewBox="0 0 232 155"
    fill="none"
    xmlns="http://www.w3.org/2000/svg"
    aria-hidden="true"
    data-mark
  >
    <path
      data-part="a"
      d="M38.6667 155L116.001 77.5L38.6682 0H0.00151042V38.75L38.6682 77.5L0 116.25V155H38.6667Z"
    />
    <path
      data-part="b"
      d="M232 116.25L193.333 155L77.3331 155L154.666 77.4999L232 77.5V116.25Z"
    />
    <path
      data-part="t"
      d="M116 38.75L154.667 6.77526e-06L232 2.03258e-05L154.666 77.4999L116 77.5L116 38.75Z"
    />
  </svg>
</template>

<style scoped>
[data-mark] path { fill: #2b7fff; transform-box: fill-box; transform-origin: center; }
@keyframes in-a { from { transform: translateX(-22px); opacity: 0; } }
@keyframes in-t { from { transform: translate(15.5px, -15.5px); opacity: 0; } }
@keyframes in-b { from { transform: translateX(22px); opacity: 0; } }
@keyframes rail { 0% { fill: #2b7fff; } 15% { fill: #51a2ff; } 45%, 100% { fill: #2b7fff; } }
[data-part="a"] { animation: in-a .55s cubic-bezier(.2, .8, .2, 1) both, rail 1.2s linear 1s infinite; }
[data-part="t"] { animation: in-t .55s cubic-bezier(.2, .8, .2, 1) .18s both, rail 1.2s linear 1.2s infinite; }
[data-part="b"] { animation: in-b .55s cubic-bezier(.2, .8, .2, 1) .35s both, rail 1.2s linear 1.4s infinite; }
@media (prefers-reduced-motion: reduce) { [data-mark] path { animation: none; } }
</style>
