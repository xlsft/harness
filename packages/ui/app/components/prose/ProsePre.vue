<script setup lang="ts">
// Fenced code block (Md.dc.html "code"): language tag, file name, Copy, line numbers, design-palette highlighting.
const props = withDefaults(defineProps<{
  code?: string
  language?: string | null
  filename?: string | null
  class?: string | null
}>(), { code: '', language: null, filename: null, class: null })

const lang = computed(() => props.language || 'text')
const shell = computed(() => ['bash', 'sh', 'shell', 'console'].includes(lang.value))
const lines = computed(() => highlightCode(props.code, lang.value))
const copied = ref(false)

async function copy() {
  await navigator.clipboard?.writeText(props.code)
  copied.value = true
  setTimeout(() => (copied.value = false), 1200)
}
</script>

<template>
  <div class="border border-default bg-surface">
    <div class="flex items-center gap-2.5 border-b border-default py-1.5 pr-2 pl-3 text-[11.5px] text-muted">
      <span class="border border-default px-[5px] text-strong">{{ lang }}</span>
      <span
        v-if="filename"
        class="truncate"
      >{{ filename }}</span>
      <span class="flex-1" />
      <button
        type="button"
        aria-label="Copy code"
        class="flex items-center gap-1.5 px-1 py-0.5 text-muted hover:text-highlighted"
        @click="copy"
      >
        <HIcon
          name="clipboard"
          :size="14"
        />{{ copied ? 'Copied' : 'Copy' }}
      </button>
    </div>
    <pre
      class="m-0 overflow-x-auto text-[12.5px] leading-[1.65] text-toned"
      :class="shell ? 'px-3.5 py-2.5' : 'py-2.5'"
    ><template
      v-for="(line, i) in lines"
      :key="i"
    ><span
v-if="!shell"
class="inline-block w-10 pr-3.5 text-right text-dimmed select-none"
>{{ i + 1 }}</span><span
      v-for="(tk, j) in line"
      :key="j"
      :style="{ color: shell && tk.t.trim() === '$' ? 'var(--color-muted)' : tk.c }"
    >{{ tk.t }}</span>{{ '\n' }}</template></pre>
  </div>
</template>
