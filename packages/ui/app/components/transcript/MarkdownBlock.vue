<script setup lang="ts">
// Assistant markdown (Md.dc.html), rendered with MDC. Typography lives in the `md-body` utility (main.css);
// fenced code blocks go through components/prose/ProsePre.vue. `streaming` adds the blinking caret.
withDefaults(defineProps<{ text: string, streaming?: boolean }>(), { streaming: false })
</script>

<template>
  <div class="md-body">
    <MDC
      :value="text"
      tag="div"
    />
    <span
      v-if="streaming"
      class="h-caret inline-block h-[15px] w-[7px] bg-accent align-[-3px]"
    />
  </div>
</template>
