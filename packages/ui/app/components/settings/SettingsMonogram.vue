<script setup lang="ts">
// Square icon of a provider preset: its logo when one is bundled (utils/providerLogos.ts, keyed by the
// preset's icon), else the icon as text ("TS", "+"). Nothing is fetched.
import { PROVIDER_LOGOS } from '~/utils/providerLogos'

const props = defineProps<{ text: string, dim?: boolean }>()
const logo = computed(() => PROVIDER_LOGOS[props.text])
</script>

<template>
  <span
    aria-hidden="true"
    class="flex size-7 shrink-0 items-center justify-center border border-line bg-raised font-mono text-[10.5px] font-semibold tracking-wide"
    :class="[dim ? 'text-dimmed' : 'text-white', logo && dim ? 'opacity-60' : '']"
  >
    <!-- Bundled, static markup (no user input). -->
    <!-- eslint-disable-next-line vue/no-v-html -->
    <span
      v-if="logo"
      class="flex size-4 [&>svg]:size-4"
      v-html="logo"
    />
    <template v-else>{{ text.slice(0, 2) }}</template>
  </span>
</template>
