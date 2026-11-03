// Themes: every theme is light or dark, and the user picks one theme per kind (Settings > General >
// Appearance). A theme sets the raw colour tokens (`--color-<token>`, the Tailwind colours: bg-surface,
// text-strong, border-line…); main.css derives Nuxt UI's `--ui-*` variables from them. To add a theme, add an
// entry with every token: the settings list it with a preview drawn from its tokens.

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

export interface Theme {
  id: string
  name: string
  kind: ThemeKind
  tokens: Record<ThemeToken, string>
}

export const THEMES: Theme[] = [
  {
    id: 'xlsft-dark',
    name: 'xlsft`s dark',
    kind: 'dark',
    tokens: {
      'ground': '#000000',
      'surface': '#0a0a0a',
      'hover': '#131313',
      'raised': '#1c1c1c',
      'line': '#2b2b2b',
      'line-strong': '#454545',
      'divider': '#5c5c5c',
      'text': '#d4d4d4',
      'muted': '#8f8f8f',
      'dimmed': '#737373',
      'strong': '#ffffff',
      'disabled': '#5c5c5c',
      'accent': '#2b7fff',
      'accent-hover': '#8ec5ff',
      'on-accent': '#ffffff',
      'ok': '#00bc7d',
      'bad': '#ff2056',
      'warn': '#fe9a00',
      'orange': '#fe6e00',
      'cyan': '#00b8db',
      'pink': '#f6339a',
      'violet': '#8e51ff',
      'chart-1': '#2b7fff',
      'chart-2': '#f54900',
      'chart-3': '#8e51ff',
      'chart-4': '#0092b8',
      'chart-5': '#f6339a',
      'graphite-50': '#ffffff',
      'graphite-100': '#f2f2f2',
      'graphite-200': '#d4d4d4',
      'graphite-300': '#b0b0b0',
      'graphite-400': '#8f8f8f',
      'graphite-500': '#737373',
      'graphite-600': '#5c5c5c',
      'graphite-700': '#454545',
      'graphite-800': '#2b2b2b',
      'graphite-900': '#131313',
      'graphite-950': '#0a0a0a'
    }
  },
  {
    id: 'xlsft-light',
    name: 'xlsft`s light',
    kind: 'light',
    tokens: {
      'ground': '#ffffff',
      'surface': '#fafafa',
      'hover': '#f2f2f2',
      'raised': '#ebebeb',
      'line': '#e3e3e3',
      'line-strong': '#c7c7c7',
      'divider': '#a8a8a8',
      'text': '#2b2b2b',
      'muted': '#6b6b6b',
      'dimmed': '#8a8a8a',
      'strong': '#000000',
      'disabled': '#b0b0b0',
      'accent': '#1f6feb',
      'accent-hover': '#0b4fc2',
      'on-accent': '#ffffff',
      'ok': '#00965f',
      'bad': '#e0003f',
      'warn': '#c26f00',
      'orange': '#d95a00',
      'cyan': '#00869f',
      'pink': '#d01f7c',
      'violet': '#6e3ae6',
      'chart-1': '#1f6feb',
      'chart-2': '#d95a00',
      'chart-3': '#6e3ae6',
      'chart-4': '#00869f',
      'chart-5': '#d01f7c',
      'graphite-50': '#000000',
      'graphite-100': '#131313',
      'graphite-200': '#2b2b2b',
      'graphite-300': '#454545',
      'graphite-400': '#6b6b6b',
      'graphite-500': '#8a8a8a',
      'graphite-600': '#a8a8a8',
      'graphite-700': '#c7c7c7',
      'graphite-800': '#e3e3e3',
      'graphite-900': '#f2f2f2',
      'graphite-950': '#fafafa'
    }
  }
]

export const DEFAULT_THEME: Record<ThemeKind, string> = { dark: 'xlsft-dark', light: 'xlsft-light' }

export type AppearanceMode = 'system' | ThemeKind

export interface Appearance {
  mode: AppearanceMode
  light: string
  dark: string
}

// localStorage keys: the choice, and the resolved variables the head script applies before the app loads
// (nuxt.config.ts), so a reload does not flash the default theme.
export const APPEARANCE_KEY = 'harness-appearance'
export const THEME_CACHE_KEY = 'harness-theme-vars'

export function themeById(id: string, kind: ThemeKind): Theme {
  return THEMES.find(t => t.id === id && t.kind === kind) ?? THEMES.find(t => t.id === DEFAULT_THEME[kind])!
}

export function themeVars(theme: Theme): Record<string, string> {
  return Object.fromEntries(THEME_TOKENS.map(k => [`--color-${k}`, theme.tokens[k]]))
}
