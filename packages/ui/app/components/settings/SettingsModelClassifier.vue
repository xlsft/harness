<script setup lang="ts">
// Classifiers page row: the chat model that decides tool calls when no classifier is in use or it is unsure.
// Saved at once.
import type { ProviderView } from '~/lib/providers/types'

const props = defineProps<{ providers: ProviderView[] }>()
const state = inject(CLASSIFIER)!
const models = computed(() => props.providers.filter(p => p.scope === 'global' && p.group === 'llm').flatMap(p => p.models.map(m => ({ value: m.id, label: m.name, hint: p.name }))))
</script>

<template>
  <div class="flex items-center gap-3 px-3 py-2.5 max-sm:flex-col max-sm:items-stretch">
    <span class="flex min-w-0 flex-1 items-center gap-3">
      <span
        aria-hidden="true"
        class="flex size-7 shrink-0 items-center justify-center border border-line bg-raised text-white"
      ><HIcon
        name="ai"
        :size="15"
      /></span>
      <span class="flex min-w-0 flex-col gap-0.5">
        <span class="text-[12.5px] text-white">Model as classifier</span>
        <span class="text-[11.5px] text-muted">A chat model decides when no classifier is in use or it is unsure.</span>
      </span>
    </span>
    <div class="w-[240px] max-sm:w-full">
      <HSelect
        :model-value="state.settings.value?.llmModel ?? ''"
        aria-label="Model as classifier"
        :disabled="!state.settings.value"
        :options="[{ value: '', label: 'Server default' }, ...models]"
        @update:model-value="state.setModel"
      />
    </div>
  </div>
</template>
