<script setup lang="ts" generic="T extends string | number">
// The tabs under a page title (Artifacts' All / Yours / Shared look).
defineProps<{ tabs: readonly { value: T, label: string }[], label?: string }>()
const model = defineModel<T>({ required: true })
</script>

<template>
  <div
    role="tablist"
    :aria-label="label"
    class="flex gap-[2px]"
  >
    <button
      v-for="t in tabs"
      :key="t.value"
      type="button"
      role="tab"
      :aria-selected="model === t.value"
      class="border px-3 py-[5px] text-[12.5px]"
      :class="model === t.value ? 'border-line bg-raised text-strong' : 'border-transparent text-toned hover:text-strong'"
      @click="model = t.value"
    >
      {{ t.label }}
    </button>
  </div>
</template>
