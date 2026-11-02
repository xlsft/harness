// https://nuxt.com/docs/api/configuration/nuxt-config
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
      htmlAttrs: { lang: 'en', class: 'dark' },
      title: 'harness',
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

  // The design is dark-only for now.
  colorMode: {
    preference: 'dark',
    fallback: 'dark'
  },

  // Code blocks are highlighted by app/utils/highlight.ts with the design's palette, not Shiki.
  mdc: {
    highlight: false,
    headings: {
      anchorLinks: false
    }
  },

  ui: {
    // Fonts come from Google Fonts (see app.head); no build-time font resolution.
    fonts: false
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
