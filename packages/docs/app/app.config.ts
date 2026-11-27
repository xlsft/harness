export default defineAppConfig({
  ui: {
    colors: {
      primary: 'blue',
      secondary: 'orange',
      success: 'emerald',
      info: 'cyan',
      warning: 'amber',
      error: 'rose',
      neutral: 'graphite'
    },
    button: { slots: { base: 'rounded-none font-normal' } },
    badge: { slots: { base: 'rounded-none font-normal' } },
    kbd: { base: 'rounded-none font-mono font-normal' },
    separator: { slots: { root: 'border-default' } }
  }
})
