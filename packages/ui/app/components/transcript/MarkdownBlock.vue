<script setup lang="ts">
// Assistant markdown (Md.dc.html), rendered with MDC. Typography lives in the `md-body` utility (main.css);
// fenced code blocks go through components/prose/ProsePre.vue. `streaming` adds the blinking caret.
withDefaults(defineProps<{ text: string, streaming?: boolean }>(), { streaming: false })
// MDC keys its cache on the first value by default: two messages that start alike while streaming would share one result.
const cacheKey = `md-${useId()}`
</script>

<template>
  <div class="md-body">
    <MDC
      :value="text"
      :cache-key="cacheKey"
      tag="div"
    />
    <span
      v-if="streaming"
      class="h-caret inline-block h-[15px] w-[7px] bg-accent align-[-3px]"
    />
  </div>
</template>
