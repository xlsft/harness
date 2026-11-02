// Demo data for the Browser pane (docs/design/ui/Browser.dc.html): tabs, the previewed dev page
// and its request chart. The agent contract has no browser yet.

export type BrowserPageKind = 'new' | 'dev' | 'pr'

export interface BrowserTab {
  id: string
  title: string
  // Favicon: a letter on a colored square.
  fav: string
  favBg: string
  favFg: string
  url: string
  kind: BrowserPageKind
}

export const NEW_TAB: Omit<BrowserTab, 'id'> = { title: 'New tab', fav: '', favBg: '#1c1c1c', favFg: '#ffffff', url: '', kind: 'new' }
export const DEV_TAB: Omit<BrowserTab, 'id'> = { title: 'harness core · providers', fav: 'h', favBg: '#2b7fff', favFg: '#ffffff', url: 'localhost:3000/providers', kind: 'dev' }
export const PR_TAB: Omit<BrowserTab, 'id'> = { title: 'xlsft/harness · PR 14', fav: 'G', favBg: '#ffffff', favFg: '#000000', url: 'github.com/xlsft/harness/pull/14', kind: 'pr' }

// The app preview offered on a new tab.
export const PREVIEW_APP = { name: 'core', detail: 'pnpm dev · :3000' }

export const PROVIDERS = [
  { name: 'OpenRouter', key: 'sk-or-…41f2', state: 'active', color: '#00bc7d' },
  { name: 'Anthropic', key: 'sk-ant-…9c0d', state: 'ready', color: '#8f8f8f' },
  { name: 'Ollama', key: 'localhost:11434', state: 'offline', color: '#fe6e00' }
]

// "Requests, last hour" bar heights in px.
export const REQUEST_BARS = [8, 12, 10, 18, 22, 16, 14, 26, 30, 24, 20, 28, 34, 40, 32, 28, 36, 44, 38, 30, 26, 34, 42, 48]

export const AGENT_STEP = { step: 3, of: 5, action: 'click · Add provider' }

export const SITE_PERMISSION = {
  title: 'Allow the agent to use github.com?',
  body: 'It wants to open the PR page and read its checks. Sign-ins in this browser stay yours.'
}

export const VIEWPORTS = [
  { name: 'Mobile', width: 375, height: 812, touch: true },
  { name: 'Tablet', width: 768, height: 1024, touch: true },
  { name: 'Desktop', width: 0, height: 0, touch: false }
] as const
