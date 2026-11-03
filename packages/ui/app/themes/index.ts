// Themes: every theme is light or dark, and the user picks one theme per kind (Settings > Appearance). A theme is
// a JSON file in this folder: every colour token (`--color-<token>`, the Tailwind colours: bg-surface,
// text-strong, border-line…) and, optionally, corner radii (`--radius-<token>`, the Tailwind radii:
// rounded-control, rounded-box…; square when left out) and fonts (`--font-sans` for the interface, `--font-mono`
// for code, `--font-display` for big titles; the monospace defaults when left out). main.css derives Nuxt UI's `--ui-*` variables from the
// colours. To add a theme, add a JSON file with every colour token and list it in THEMES below; the settings show
// it with a preview drawn from its tokens.
import cloudyMorningDark from './cloudy-morning-dark.json'
import cloudyMorningLight from './cloudy-morning-light.json'
import xlsftDark from './xlsft-dark.json'
import xlsftLight from './xlsft-light.json'

export type ThemeKind = 'light' | 'dark'

export const THEME_TOKENS = [
  // Surfaces, from the page outwards.
  'ground', 'surface', 'hover', 'raised',
  // Borders inside a block, stronger borders, and the dividers between the layout's blocks.
  'line', 'line-strong', 'divider',
  // Text: body, secondary, tertiary, headings and values, disabled.
  'text', 'muted', 'dimmed', 'strong', 'disabled',
  // Accent and the text on accent (and other solid colour) backgrounds.
  'accent', 'accent-hover', 'on-accent',
  // Status and highlight colours.
  'ok', 'bad', 'warn', 'orange', 'cyan', 'pink', 'violet',
  // Chart series in order (identity, not status), checked for colour-blind separation on the theme's surface.
  'chart-1', 'chart-2', 'chart-3', 'chart-4', 'chart-5',
  // Neutral scale for Nuxt UI (`neutral`), lightest to darkest on screen.
  'graphite-50', 'graphite-100', 'graphite-200', 'graphite-300', 'graphite-400', 'graphite-500',
  'graphite-600', 'graphite-700', 'graphite-800', 'graphite-900', 'graphite-950'
] as const

export type ThemeToken = typeof THEME_TOKENS[number]

export const RADIUS_TOKENS = [
  // Badges, chips, kbd hints, small marks.
  'chip',
  // Buttons, inputs, selects, menu rows.
  'control',
  // Cards, message bubbles, the composer, tool output, menus and popovers.
  'box',
  // Modals and other large surfaces.
  'panel'
] as const

export type RadiusToken = typeof RADIUS_TOKENS[number]

// The default fonts (main.css @theme): monospace everywhere.
const MONO = '\'Cascadia Mono\', \'Cascadia Code\', \'JetBrains Mono\', ui-monospace, monospace'
export const DEFAULT_FONTS = { sans: MONO, mono: MONO, display: '\'Cascadia Code\', \'JetBrains Mono\', ui-monospace, monospace' }
export type FontToken = keyof typeof DEFAULT_FONTS

export interface Theme {
  id: string
  name: string
  kind: ThemeKind
  tokens: Record<ThemeToken, string>
  // CSS lengths; a token left out is square (0).
  radius?: Partial<Record<RadiusToken, string>>
  // CSS font-family lists; a font left out is the default.
  fonts?: Partial<Record<FontToken, string>>
}

export const THEMES = [xlsftDark, xlsftLight, cloudyMorningDark, cloudyMorningLight] as Theme[]

export const DEFAULT_THEME: Record<ThemeKind, string> = { dark: 'xlsft-dark', light: 'xlsft-light' }

export type AppearanceMode = 'system' | ThemeKind

export interface Appearance {
  mode: AppearanceMode
  light: string
  dark: string
  // Interface text size in px (the whole app scales from 13); code size in px (0: the default 12.5); a font
  // family for code put before the theme's (''; the theme's).
  uiSize: number
  codeSize: number
  codeFont: string
}

export const UI_SIZE_DEFAULT = 13
export const CODE_SIZE_DEFAULT = 12.5

// localStorage keys: the choice, and the resolved variables the head script applies before the app loads
// (nuxt.config.ts), so a reload does not flash the default theme.
export const APPEARANCE_KEY = 'harness-appearance'
export const THEME_CACHE_KEY = 'harness-theme-vars'
// Themes imported in this browser (VS Code themes, themes/vscode.ts).
export const CUSTOM_THEMES_KEY = 'harness-custom-themes'

// `extra`: imported themes, looked up after the built-in ones.
export function themeById(id: string, kind: ThemeKind, extra: Theme[] = []): Theme {
  return [...THEMES, ...extra].find(t => t.id === id && t.kind === kind) ?? THEMES.find(t => t.id === DEFAULT_THEME[kind])!
}

export function themeVars(theme: Theme): Record<string, string> {
  return Object.fromEntries([
    ...THEME_TOKENS.map(k => [`--color-${k}`, theme.tokens[k]]),
    ...RADIUS_TOKENS.map(k => [`--radius-${k}`, theme.radius?.[k] ?? '0px']),
    ...(Object.keys(DEFAULT_FONTS) as FontToken[]).map(k => [`--font-${k}`, theme.fonts?.[k] ?? DEFAULT_FONTS[k]])
  ])
}
