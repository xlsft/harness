<script setup lang="ts">
// Fenced code block (Md.dc.html "code"): language tag, file name, Copy, line numbers, design-palette highlighting.
// Shell blocks also get Run: the command runs in the open session's shell (as the composer's shell mode does).
import type { UserShellItem } from '~/lib/session/types'

const props = withDefaults(defineProps<{
  code?: string
  language?: string | null
  filename?: string | null
  class?: string | null
}>(), { code: '', language: null, filename: null, class: null })

const lang = computed(() => props.language || 'text')
const shell = computed(() => ['bash', 'sh', 'zsh', 'shell', 'console'].includes(lang.value))
const lines = computed(() => highlightCode(props.code, lang.value))
const copied = ref(false)

const store = useSessionStore()
const toast = useToast()
// Only for a session of this computer; "$ " prompts in front of commands are dropped.
const runnable = computed(() => shell.value && !!store.current && !store.current.meta.remote)
// The run's output shows under the code; when it finishes the agent answers with it (store.runShell inline).
const commandId = ref('')
const blockCommand = computed(() => props.code.split('\n').map(l => l.replace(/^\s*\$ /, '')).join('\n').trim())
// After a reload the block finds its last run again by its command.
const result = computed(() => {
  const items = store.current?.items ?? []
  if (commandId.value) return items.find(i => i.kind === 'shell' && i.id === commandId.value) as UserShellItem | undefined
  return items.findLast(i => i.kind === 'shell' && store.inlineShells[i.id]?.command === blockCommand.value) as UserShellItem | undefined
})
const running = computed(() => commandId.value ? !result.value || result.value.state === 'running' : result.value?.state === 'running')
async function run() {
  const s = store.current
  if (!s || running.value) return
  try {
    commandId.value = await store.runShell(s.meta.id, blockCommand.value, { inline: true })
  } catch (e) {
    toast.add({ title: 'Could not run the command', description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
  }
}

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
        v-if="runnable"
        type="button"
        aria-label="Run in the session's shell"
        title="Run in the session's shell"
        class="flex items-center gap-1.5 px-1 py-0.5 text-muted hover:text-highlighted"
        @click="run"
      >
        <HIcon
          :name="running ? 'loading' : 'play'"
          :spin="running"
          :size="14"
        />{{ running ? 'Running' : 'Run' }}
      </button>
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
    <div
      v-if="result && (result.output || result.stderr || result.state !== 'running')"
      class="border-t border-default"
    >
      <div class="flex items-center gap-2 px-3.5 pt-2 text-[11px] text-muted">
        <span>Output</span><span class="flex-1" /><span
          v-if="result.state !== 'running'"
          :class="result.exitCode === 0 ? 'text-ok' : 'text-bad'"
        >{{ result.state === 'stopped' ? 'stopped' : result.state === 'timeout' ? 'time limit' : `exit ${result.exitCode}` }} · {{ formatSeconds(result.durationMs) }}</span>
      </div>
      <pre class="m-0 max-h-72 overflow-auto px-3.5 py-2 text-[12px] leading-[1.6] whitespace-pre-wrap text-toned">{{ result.output }}<span
        v-if="result.stderr"
        class="text-bad"
      >{{ result.stderr }}</span><span
        v-if="result.error"
        class="text-bad"
      >{{ result.error }}</span></pre>
    </div>
  </div>
</template>
