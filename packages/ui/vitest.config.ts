import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

// The reducer and the mock transport are plain TypeScript (no Nuxt runtime), so they run in a node environment.
export default defineConfig({
  resolve: {
    alias: { '~': fileURLToPath(new URL('./app', import.meta.url)) }
  },
  test: {
    environment: 'node',
    include: ['test/**/*.test.ts']
  }
})
