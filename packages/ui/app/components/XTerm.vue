<script setup lang="ts">
// One terminal screen: xterm.js drawn on the GPU (WebGL addon; the DOM renderer when WebGL is unavailable or its
// context is lost), sized to its box (fit addon), with links clickable and the colours, font and code size of the
// current theme. It only draws: the parent feeds output with write() and gets keystrokes from `data`, and the
// terminal's size from `resize`.
import '@xterm/xterm/css/xterm.css'
import type { Terminal, ITheme } from '@xterm/xterm'

const props = withDefaults(defineProps<{ cursorStyle?: 'block' | 'bar' | 'underline', cursorBlink?: boolean }>(), { cursorStyle: 'block', cursorBlink: true })
const emit = defineEmits<{ data: [data: string], resize: [cols: number, rows: number], title: [title: string] }>()

const host = ref<HTMLElement | null>(null)
const { theme, appearance } = useAppearance()
let term: Terminal | null = null
let fit: { fit: () => void } | null = null
let observer: ResizeObserver | undefined
const pending: (string | Uint8Array)[] = []

// ANSI colours from the theme's tokens: the normal set, and brighter ones mixed toward the strong text colour.
function xtermTheme(): ITheme {
  const t = theme.value.tokens
  const bright = (c: string) => `color-mix(in srgb, ${c} 75%, ${t.strong})`
  const css = (v: string) => {
    // xterm needs concrete colours: resolve color-mix() through a probe element.
    const probe = document.createElement('span')
    probe.style.color = v
    document.body.appendChild(probe)
    const out = getComputedStyle(probe).color
    probe.remove()
    return out
  }
  return {
    background: t.surface,
    foreground: t.text,
    cursor: t.strong,
    cursorAccent: t.surface,
    selectionBackground: css(`color-mix(in srgb, ${t.accent} 35%, transparent)`),
    black: t.raised,
    red: t.bad,
    green: t.ok,
    yellow: t.warn,
    blue: t.accent,
    magenta: t.pink,
    cyan: t.cyan,
    white: t.text,
    brightBlack: t.dimmed,
    brightRed: css(bright(t.bad)),
    brightGreen: css(bright(t.ok)),
    brightYellow: css(bright(t.warn)),
    brightBlue: css(bright(t.accent)),
    brightMagenta: css(bright(t.pink)),
    brightCyan: css(bright(t.cyan)),
    brightWhite: t.strong
  }
}

const fontFamily = () => getComputedStyle(document.documentElement).getPropertyValue('--font-terminal').trim() || 'ui-monospace, monospace'
// xterm measures its cells once: load the terminal font (and its bold) before opening or switching to it.
async function loadFont() {
  const family = fontFamily()
  await Promise.all([`${fontSize()}px ${family}`, `bold ${fontSize()}px ${family}`].map(f => document.fonts.load(f).catch(() => [])))
}
const fontSize = () => appearance.value.codeSize || 12.5

onMounted(async () => {
  const [{ Terminal }, { FitAddon }, { WebglAddon }, { WebLinksAddon }] = await Promise.all([
    import('@xterm/xterm'),
    import('@xterm/addon-fit'),
    import('@xterm/addon-webgl'),
    import('@xterm/addon-web-links')
  ])
  await loadFont()
  if (!host.value) return
  term = new Terminal({
    allowProposedApi: true,
    cursorStyle: props.cursorStyle,
    cursorBlink: props.cursorBlink,
    fontFamily: fontFamily(),
    fontSize: fontSize(),
    lineHeight: 1.2,
    scrollback: 10_000,
    theme: xtermTheme(),
    macOptionIsMeta: true
  })
  const fitAddon = new FitAddon()
  fit = fitAddon
  term.loadAddon(fitAddon)
  term.loadAddon(new WebLinksAddon((_e, uri) => window.open(uri, '_blank', 'noopener')))
  term.open(host.value)
  try {
    const webgl = new WebglAddon()
    webgl.onContextLoss(() => webgl.dispose())
    term.loadAddon(webgl)
  } catch {
    // No WebGL here: xterm keeps its DOM renderer.
  }
  term.onData(d => emit('data', d))
  term.onResize(({ cols, rows }) => emit('resize', cols, rows))
  term.onTitleChange(t => emit('title', t))
  for (const chunk of pending.splice(0)) term.write(chunk)
  observer = new ResizeObserver(() => {
    try {
      fitAddon.fit()
    } catch { /* hidden pane: nothing to measure */ }
  })
  observer.observe(host.value)
  fitAddon.fit()
  emit('resize', term.cols, term.rows)
})

watch([theme, () => appearance.value.codeSize, () => appearance.value.terminalFont], async () => {
  if (!term) return
  await loadFont()
  if (!term) return
  term.options.theme = xtermTheme()
  term.options.fontSize = fontSize()
  term.options.fontFamily = fontFamily()
  fit?.fit()
})
watch(() => [props.cursorStyle, props.cursorBlink] as const, ([style, blink]) => {
  if (!term) return
  term.options.cursorStyle = style
  term.options.cursorBlink = blink
})

onBeforeUnmount(() => {
  observer?.disconnect()
  term?.dispose()
  term = null
})

defineExpose({
  write(data: string | Uint8Array) {
    if (term) term.write(data)
    else pending.push(data)
  },
  focus: () => term?.focus(),
  clear: () => term?.clear(),
  reset: () => term?.reset(),
  size: () => term ? { cols: term.cols, rows: term.rows } : null
})
</script>

<template>
  <div
    ref="host"
    data-xterm-host
    class="size-full min-h-0 min-w-0 overflow-hidden p-2"
    :style="{ background: theme.tokens.surface }"
  />
</template>

<style scoped>
[data-xterm-host] :deep(.xterm) {
  height: 100%;
}
[data-xterm-host] :deep(.xterm-viewport) {
  background: transparent !important;
}
</style>
