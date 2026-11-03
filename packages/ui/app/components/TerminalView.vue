<script setup lang="ts">
// One live terminal: an XTerm screen wired to the client's PTY over its socket (lib/terminal). Reconnects replay the
// recent output, so the screen is reset before each replay. The pane keeps a view per tab and hides the others.
import type { TerminalConnection, TerminalConnectionState } from '~/lib/terminal/connection'
import type { TerminalInfo } from '~/lib/terminal/types'

const props = defineProps<{ info: TerminalInfo, ticket?: string, active: boolean, cursorStyle: 'block' | 'bar' | 'underline', cursorBlink: boolean }>()
const emit = defineEmits<{ exit: [code: number], state: [state: TerminalConnectionState] }>()

const store = useSessionStore()
const screen = ref<{ write: (d: string | Uint8Array) => void, reset: () => void, focus: () => void, clear: () => void } | null>(null)
let conn: TerminalConnection | null = null
const offs: (() => void)[] = []

onMounted(() => {
  if (!store.transport) return
  conn = store.transport.terminals().connect(props.info.id, props.ticket)
  offs.push(
    conn.onAttach(() => screen.value?.reset()),
    conn.onData(d => screen.value?.write(d)),
    conn.onExit(code => emit('exit', code)),
    conn.onStateChange(s => emit('state', s))
  )
})
onBeforeUnmount(() => {
  for (const off of offs) off()
  conn?.close()
})

const write = (d: string) => conn?.write(d)
const resize = (cols: number, rows: number) => conn?.resize(cols, rows)

watch(() => props.active, (v) => {
  if (v) nextTick(() => screen.value?.focus())
}, { immediate: true })

defineExpose({ clear: () => screen.value?.clear(), focus: () => screen.value?.focus() })
</script>

<template>
  <XTerm
    ref="screen"
    :cursor-style="cursorStyle"
    :cursor-blink="cursorBlink"
    @data="write"
    @resize="resize"
  />
</template>
