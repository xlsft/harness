import { fileURLToPath } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

// The reducer and the mock transport are plain TypeScript (no Nuxt runtime), so they run in a node environment. A component test
// asks for happy-dom in its first line.
export default defineConfig({
  // Component tests mount real .vue files (test/helpers/mountPage.ts); the rest stay plain TypeScript.
  plugins: [vue()],
  resolve: {
    alias: { '~': fileURLToPath(new URL('./app', import.meta.url)) }
  },
  test: {
    environment: 'node',
    include: ['test/**/*.test.ts']
  }
})
