// @ts-check
import withNuxt from './.nuxt/eslint.config.mjs'
import betterTailwindcss from 'eslint-plugin-better-tailwindcss'
import { getDefaultAttributes } from 'eslint-plugin-better-tailwindcss/api/defaults'

export default withNuxt(
  betterTailwindcss.configs['correctness-error'],
  {
    ignores: ['app/gen/**', 'testdata/**']
  },
  {
    // The design names these single-word components (Composer, Transcript).
    files: ['app/components/Composer.vue', 'app/components/Transcript.vue'],
    rules: { 'vue/multi-word-component-names': 'off' }
  },
  {
    settings: {
      'better-tailwindcss': {
        entryPoint: 'app/assets/css/main.css',
        attributes: [
          ...getDefaultAttributes(),
          ['^v-bind:ui$', [{ match: 'objectValues' }]]
        ]
      }
    }
  }
)
