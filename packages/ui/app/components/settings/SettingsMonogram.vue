<script setup lang="ts">
// Square icon of a provider preset: its logo when one is bundled (utils/providerLogos.ts, keyed by the
// preset's icon), an app icon (`icon`, e.g. custom providers), else the text ("TS"). Nothing is fetched.
import { PROVIDER_LOGOS } from '~/utils/providerLogos'
import type { IconName } from '~/utils/icons'

const props = defineProps<{ text?: string, icon?: IconName, dim?: boolean }>()
const logo = computed(() => (props.text ? PROVIDER_LOGOS[props.text] : undefined))
</script>

<template>
  <span
    aria-hidden="true"
    class="flex size-7 shrink-0 items-center justify-center border border-line bg-raised font-mono text-[10.5px] font-semibold tracking-wide"
    :class="[dim ? 'text-dimmed' : 'text-strong', logo && dim ? 'opacity-60' : '']"
  >
    <!-- Bundled, static markup (no user input). -->
    <!-- eslint-disable-next-line vue/no-v-html -->
    <span
      v-if="logo"
      class="flex size-4 [&>svg]:size-4"
      v-html="logo"
    />
    <HIcon
      v-else-if="icon"
      :name="icon"
      :size="15"
    />
    <template v-else>{{ (text ?? '').slice(0, 2) }}</template>
  </span>
</template>
