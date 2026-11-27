// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  modules: ['@nuxt/ui', '@nuxt/eslint'],
  ssr: true,
  devtools: { enabled: false },
  app: {
    head: {
      htmlAttrs: { lang: 'en' },
      title: "xlsft's harness — your workflow, unbounded",
      meta: [
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
        { name: 'description', content: 'The coding agent you already know, with any model and a server you own. Web + macOS.' },
        { name: 'theme-color', content: '#000000' }
      ],
      link: [
        { rel: 'icon', type: 'image/svg+xml', href: '/logo.svg' },
        { rel: 'preconnect', href: 'https://fonts.googleapis.com' },
        { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' },
        { rel: 'stylesheet', href: 'https://fonts.googleapis.com/css2?family=Cascadia+Code:wght@400;600;700&family=JetBrains+Mono:wght@400;500;700&display=swap' }
      ]
    }
  },
  css: ['~/assets/css/main.css'],
  ui: { fonts: false, colorMode: false },
  compatibilityDate: '2026-06-30',
  nitro: { prerender: { routes: ['/'] } }
})
