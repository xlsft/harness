<script setup lang="ts">
// A command you ran in the composer's shell mode (RunShellCommand): drawn like the Bash tool card, but marked
// as yours (terminal icon, "You ran"). The client ran it, not the model; the model sees the command and its
// output on its next request. Output streams while it runs (Stop kills it), then stdout and stderr; it collapses.
import type { UserShellItem } from '~/lib/session/types'

const props = defineProps<{ item: UserShellItem, sessionId: string }>()
const store = useSessionStore()
const now = useNow()
const open = ref(true)
const stopping = ref(false)

const running = computed(() => props.item.state === 'running')
const failed = computed(() => props.item.state === 'error' || props.item.state === 'failed' || props.item.state === 'timeout')
const stdoutLines = computed(() => props.item.output ? highlightOutput(props.item.output) : [])
const stderrLines = computed(() => stripTerminalCodes(props.item.stderr).replace(/\n$/, '').split('\n').filter((l, i, all) => l || i < all.length - 1))
const hasBody = computed(() => !!props.item.output || !!props.item.stderr || !!props.item.error || props.item.truncated)

const status = computed(() => {
  const it = props.item
  switch (it.state) {
    case 'running': return { text: `running · ${formatDuration(now.value - it.startedAt)}`, cls: 'text-accent' }
    case 'stopped': return { text: `stopped · ${formatSeconds(it.durationMs)}`, cls: 'text-muted' }
    case 'timeout': return { text: `timed out · ${formatSeconds(it.durationMs)}`, cls: 'text-bad' }
    case 'failed': return { text: 'could not run', cls: 'text-bad' }
    default: return { text: `exit ${it.exitCode} · ${formatSeconds(it.durationMs)}`, cls: it.exitCode === 0 ? 'text-ok' : 'text-bad' }
  }
})

async function stop() {
  stopping.value = true
  try {
    await store.stopShell(props.sessionId, props.item.id)
  } catch {
    // Already finished: the Finished event updates the card.
  } finally {
    stopping.value = false
  }
}
</script>

<template>
  <div
    class="border border-default"
    :class="failed ? 'border-l-2 border-l-bad' : ''"
  >
    <div
      class="flex items-center gap-[9px] px-3 py-[7px] text-[12.5px] text-muted"
      :class="hasBody && open ? 'border-b border-default' : ''"
    >
      <HIcon
        name="terminal"
        :class="running ? 'text-accent' : failed ? 'text-bad' : 'text-orange'"
      />
      <span class="shrink-0">{{ tt('You ran') }}</span>
      <span
        class="min-w-0 truncate font-mono text-strong"
        :title="item.cwd ? `${item.command}\nin ${item.cwd}` : item.command"
      >{{ item.command }}</span>
      <span class="flex-1" />
      <span
        class="shrink-0 text-[11px]"
        :class="status.cls"
      >{{ status.text }}</span>
      <HBtn
        v-if="running"
        tone="danger"
        :disabled="stopping"
        @click="stop"
      >
        {{ tt('Stop') }}
      </HBtn>
      <button
        v-else-if="hasBody"
        type="button"
        :aria-label="open ? tt('Collapse output') : tt('Expand output')"
        class="text-muted hover:text-highlighted"
        @click="open = !open"
      >
        <HIcon
          name="down"
          :size="14"
          :class="open ? '' : '-rotate-90'"
        />
      </button>
    </div>

    <pre
      v-if="open && hasBody"
      class="m-0 max-h-80 overflow-auto bg-surface px-3 py-[9px] text-[12px] leading-[1.6]"
      :class="running ? 'text-muted' : 'text-toned'"
    ><template
      v-for="(line, i) in stdoutLines"
      :key="'o' + i"
    ><span
      v-for="(tk, j) in line"
      :key="j"
      :style="{ color: running ? undefined : tk.c }"
    >{{ tk.t }}</span>{{ '\n' }}</template><span
      v-for="(line, i) in stderrLines"
      :key="'e' + i"
      class="text-bad"
    >{{ line }}{{ '\n' }}</span><span
      v-if="item.error"
      class="text-bad"
    >{{ item.error }}{{ '\n' }}</span><span
      v-if="item.truncated"
      class="text-dimmed"
    >[long output: the middle was cut]{{ '\n' }}</span><span
      v-if="running"
      class="h-caret ml-0.5 inline-block h-[13px] w-[7px] bg-graphite-200 align-[-2px]"
    /></pre>
    <div
      v-if="open && item.cwd && !running"
      class="border-t border-default px-3 py-1 text-[11px] text-dimmed"
    >
      <span class="font-mono">{{ item.cwd }}</span>
    </div>
  </div>
</template>
