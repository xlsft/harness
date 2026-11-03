// VS Code colour themes → app themes. Reads a theme file (JSON with comments and trailing commas, as VS Code
// writes them), takes its kind from `type` and maps its workbench colours (`colors`) onto the app's tokens; a
// colour the theme leaves out is derived from the ones it has, or taken from the default theme of the same kind.
// Translucent colours are laid over the background. VS Code is square, so imported themes have no radii.
import { DEFAULT_THEME, THEME_TOKENS, themeById, type Theme, type ThemeKind, type ThemeToken } from './index'

type Rgb = [number, number, number]

function parseHex(v: unknown): { rgb: Rgb, a: number } | null {
  if (typeof v !== 'string') return null
  const m = v.trim().match(/^#([0-9a-f]{3,8})$/i)
  if (!m) return null
  let h = m[1]!
  if (h.length === 3 || h.length === 4) h = [...h].map(c => c + c).join('')
  if (h.length !== 6 && h.length !== 8) return null
  const n = (i: number) => parseInt(h.slice(i, i + 2), 16)
  return { rgb: [n(0), n(2), n(4)], a: h.length === 8 ? n(6) / 255 : 1 }
}

const hex = (c: Rgb) => '#' + c.map(x => Math.round(Math.min(255, Math.max(0, x))).toString(16).padStart(2, '0')).join('')
const mix = (a: Rgb, b: Rgb, t: number): Rgb => [0, 1, 2].map(i => a[i]! + (b[i]! - a[i]!) * t) as Rgb
const luminance = (c: Rgb) => {
  const [r, g, b] = c.map((x) => {
    const s = x / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!
}

// Strips // and /* */ comments (outside strings) and trailing commas, then parses.
export function parseJsonc(text: string): unknown {
  let out = ''
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (c === '"') {
      const start = i
      for (i++; i < text.length && text[i] !== '"'; i++) if (text[i] === '\\') i++
      out += text.slice(start, i + 1)
    } else if (c === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++
      out += '\n'
    } else if (c === '/' && text[i + 1] === '*') {
      i = text.indexOf('*/', i + 2)
      if (i < 0) break
      i++
    } else out += c
  }
  return JSON.parse(out.replace(/,(\s*[}\]])/g, '$1'))
}

const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40) || 'theme'

export function themeFromVsCode(source: string, fileName = ''): Theme {
  const raw = parseJsonc(source) as { name?: unknown, type?: unknown, colors?: Record<string, unknown> }
  if (!raw || typeof raw !== 'object' || !raw.colors || typeof raw.colors !== 'object') {
    throw new Error('Not a VS Code colour theme: it has no "colors".')
  }
  const colors = raw.colors
  const fallback = themeById(DEFAULT_THEME.dark, 'dark')
  const bgRaw = parseHex(colors['editor.background'])?.rgb ?? parseHex(fallback.tokens.ground)!.rgb
  // The kind: what the theme says, else what its background looks like.
  const kind: ThemeKind = raw.type === 'light' || raw.type === 'hcLight'
    ? 'light'
    : raw.type === 'dark' || raw.type === 'hc' || raw.type === 'hcDark' ? 'dark' : luminance(bgRaw) > 0.4 ? 'light' : 'dark'
  const base = themeById(DEFAULT_THEME[kind], kind)
  const ground = bgRaw
  const toward: Rgb = kind === 'dark' ? [255, 255, 255] : [0, 0, 0]

  // The first colour the theme sets among `keys`, laid over the background when translucent.
  const pick = (...keys: string[]): Rgb | null => {
    for (const k of keys) {
      const c = parseHex(colors[k])
      if (c) return c.a < 1 ? mix(ground, c.rgb, c.a) : c.rgb
    }
    return null
  }
  const text = pick('editor.foreground', 'foreground') ?? parseHex(base.tokens.text)!.rgb
  const step = (t: number) => mix(ground, toward, t)

  const t: Partial<Record<ThemeToken, Rgb>> = {}
  t.ground = ground
  t.surface = pick('sideBar.background', 'editorGroupHeader.tabsBackground', 'panel.background') ?? step(0.03)
  t.hover = pick('list.hoverBackground', 'toolbar.hoverBackground') ?? step(0.06)
  t.raised = pick('input.background', 'dropdown.background', 'editorWidget.background') ?? step(0.1)
  t.line = pick('panel.border', 'editorGroup.border', 'sideBar.border', 'widget.border') ?? mix(ground, text, 0.15)
  t['line-strong'] = pick('input.border', 'dropdown.border', 'contrastBorder') ?? mix(ground, text, 0.28)
  t.divider = pick('editorGroup.border', 'panel.border') ?? mix(ground, text, 0.3)
  t.text = text
  t.strong = mix(text, toward, 0.45)
  t.muted = pick('descriptionForeground', 'tab.inactiveForeground') ?? mix(text, ground, 0.35)
  t.dimmed = pick('editorLineNumber.foreground') ?? mix(text, ground, 0.5)
  t.disabled = pick('disabledForeground') ?? mix(text, ground, 0.6)
  const accent = pick('button.background', 'focusBorder', 'textLink.foreground', 'activityBarBadge.background') ?? parseHex(base.tokens.accent)!.rgb
  t.accent = accent
  t['accent-hover'] = pick('button.hoverBackground') ?? mix(accent, toward, 0.2)
  t['on-accent'] = pick('button.foreground') ?? (luminance(accent) > 0.45 ? [0, 0, 0] : [255, 255, 255])
  const ansi = (name: string, token: ThemeToken) => pick(`terminal.ansi${name}`) ?? parseHex(base.tokens[token])!.rgb
  t.ok = pick('terminal.ansiGreen', 'gitDecoration.addedResourceForeground') ?? parseHex(base.tokens.ok)!.rgb
  t.bad = pick('errorForeground', 'editorError.foreground', 'terminal.ansiRed') ?? parseHex(base.tokens.bad)!.rgb
  t.warn = pick('editorWarning.foreground', 'terminal.ansiYellow') ?? parseHex(base.tokens.warn)!.rgb
  t.cyan = ansi('Cyan', 'cyan')
  t.pink = ansi('Magenta', 'pink')
  t.orange = mix(ansi('Red', 'bad'), ansi('Yellow', 'warn'), 0.5)
  t.violet = mix(ansi('Blue', 'accent'), ansi('Magenta', 'pink'), 0.5)
  t['chart-1'] = accent
  t['chart-2'] = t.orange
  t['chart-3'] = t.violet
  t['chart-4'] = t.cyan
  t['chart-5'] = t.pink
  // Neutral scale from the strongest text to the surface.
  const ends: [Rgb, Rgb] = [t.strong, t.surface]
  const scale = ['50', '100', '200', '300', '400', '500', '600', '700', '800', '900', '950'] as const
  scale.forEach((s, i) => (t[`graphite-${s}`] = mix(ends[0], ends[1], i / (scale.length - 1))))

  const tokens = Object.fromEntries(THEME_TOKENS.map(k => [k, t[k] ? hex(t[k]!) : base.tokens[k]])) as Theme['tokens']
  const name = typeof raw.name === 'string' && raw.name.trim() ? raw.name.trim() : fileName.replace(/\.json$/i, '') || 'Imported theme'
  return { id: `vscode-${slug(name)}`, name, kind, tokens }
}
