<script setup lang="ts">
// A short diff in a theme's colours, with the chosen code font and size: how code blocks and diffs will look.
// Colours follow utils/highlight.ts (keywords pink, strings green, types cyan, calls strong).
import type { Theme } from '~/themes'

const props = defineProps<{ theme: Theme, size: number, font: string }>()
const c = computed(() => props.theme.tokens)

type Part = [text: string, token: keyof Theme['tokens']]
const LINES: { n: string, mark: '' | '+' | '-', parts: Part[] }[] = [
  { n: '1', mark: '', parts: [['function ', 'pink'], ['greet', 'strong'], ['(name: ', 'text'], ['string', 'cyan'], [') {', 'text']] },
  { n: '2', mark: '-', parts: [['  return ', 'pink'], ['"Hello, "', 'ok'], [' + name;', 'text']] },
  { n: '2', mark: '+', parts: [['  return ', 'pink'], ['`Hello, ${name}!`', 'ok'], [';', 'text']] },
  { n: '3', mark: '', parts: [['}', 'text']] }
]
const tint = (token: 'ok' | 'bad') => `color-mix(in srgb, ${c.value[token]} 14%, ${c.value.surface})`
</script>

<template>
  <div
    class="overflow-hidden border py-2"
    :style="{ background: c.surface, borderColor: c.line, borderRadius: theme.radius?.box, fontFamily: font, fontSize: `${size}px`, lineHeight: 1.65 }"
    aria-hidden="true"
  >
    <div
      v-for="(l, i) in LINES"
      :key="i"
      class="flex whitespace-pre"
      :style="{ background: l.mark === '+' ? tint('ok') : l.mark === '-' ? tint('bad') : 'transparent' }"
    >
      <span
        class="w-[3px] shrink-0"
        :style="{ background: l.mark === '+' ? c.ok : l.mark === '-' ? c.bad : 'transparent' }"
      />
      <span
        class="w-8 shrink-0 pr-3 text-right"
        :style="{ color: c.dimmed }"
      >{{ l.n }}</span>
      <span
        v-for="(p, j) in l.parts"
        :key="j"
        :style="{ color: c[p[1]] }"
      >{{ p[0] }}</span>
    </div>
  </div>
</template>
