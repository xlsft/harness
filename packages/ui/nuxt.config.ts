// https://nuxt.com/docs/api/configuration/nuxt-config
import { APPEARANCE_KEY, DEFAULT_THEME, THEME_CACHE_KEY, themeById, themeVars } from './app/themes'

// Runs before the app: puts the theme the last load used (or the default one of the system's kind) on <html>,
// so the page does not flash the other theme. composables/useAppearance.ts takes over once the app starts.
const DEFAULT_VARS = { light: themeVars(themeById(DEFAULT_THEME.light, 'light')), dark: themeVars(themeById(DEFAULT_THEME.dark, 'dark')) }
const THEME_SCRIPT = `(function(){try{var d=${JSON.stringify(DEFAULT_VARS)},a={},c=null;
try{a=JSON.parse(localStorage.getItem(${JSON.stringify(APPEARANCE_KEY)})||'{}')}catch(e){}
try{c=JSON.parse(localStorage.getItem(${JSON.stringify(THEME_CACHE_KEY)})||'null')}catch(e){}
var k=a.mode==='light'||a.mode==='dark'?a.mode:(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');
var v=c&&c.kind===k&&c.vars?c.vars:d[k],r=document.documentElement;
for(var n in v)r.style.setProperty(n,v[n]);r.classList.add(k)}catch(e){}})()`

export default defineNuxtConfig({
  // The Connect transport is loaded lazily; pre-bundle its deps so the first dev load does not reload.
  modules: [
    '@nuxt/eslint',
    '@nuxt/ui',
    '@nuxtjs/mdc',
    '@pinia/nuxt'
  ],

  // The UI is a client app talking to a local agent: no server rendering needed.
  ssr: false,

  // Components are named by file, not by folder (transcript/ToolCall.vue is <ToolCall>).
  // Prose* are global so MDC renders markdown with them instead of Nuxt UI's prose components.
  components: [
    { path: '~/components/prose', pathPrefix: false, global: true, priority: 10 },
    { path: '~/components', pathPrefix: false }
  ],

  devtools: {
    enabled: false
  },

  app: {
    head: {
      htmlAttrs: { lang: 'en' },
      script: [{ innerHTML: THEME_SCRIPT, tagPriority: 'critical' }],
      title: 'xlsft`s harness',
      meta: [
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
        { name: 'theme-color', content: '#000000' }
      ],
      link: [
        { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' },
        { rel: 'preconnect', href: 'https://fonts.googleapis.com' },
        { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' },
        {
          rel: 'stylesheet',
          href: 'https://fonts.googleapis.com/css2?family=Cascadia+Code:wght@400;600;700&family=JetBrains+Mono:wght@400;700&display=swap'
        }
      ]
    }
  },

  css: ['~/assets/css/main.css'],

  // Code blocks are highlighted by app/utils/highlight.ts with the design's palette, not Shiki.
  mdc: {
    highlight: false,
    headings: {
      anchorLinks: false
    }
  },

  ui: {
    // Fonts come from Google Fonts (see app.head); no build-time font resolution.
    fonts: false,
    // Themes and the light / dark class are ours (composables/useAppearance.ts), not @nuxtjs/color-mode's.
    colorMode: false
  },

  runtimeConfig: {
    public: {
      // AgentService base URL for the Connect transport (NUXT_PUBLIC_AGENT_URL).
      agentUrl: '',
      // Bearer token of `harness-client serve --listen` (NUXT_PUBLIC_AGENT_TOKEN). An `?agentToken=` query
      // parameter overrides it for the tab (kept in sessionStorage).
      agentToken: '',
      // "mock" (default) replays scripted harness.agent.v1 events; "connect" talks to agentUrl.
      transport: 'mock'
    }
  },

  compatibilityDate: '2026-06-30',
  vite: {
    optimizeDeps: {
      include: ['@bufbuild/protobuf', '@bufbuild/protobuf/codegenv2', '@connectrpc/connect', '@connectrpc/connect-web']
    }
  },

  eslint: {
    config: {
      stylistic: {
        commaDangle: 'never',
        braceStyle: '1tbs'
      }
    }
  }
})
