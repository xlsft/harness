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
    // Square, hairline, monospace: every Nuxt UI component is restyled to the design.
    button: {
      slots: {
        base: 'rounded-none font-normal'
      }
    },
    tooltip: {
      slots: {
        content: 'rounded-none bg-elevated ring ring-accented text-[11px] text-highlighted h-auto px-2 py-1 shadow-none gap-1.5',
        kbds: 'gap-0.5'
      }
    },
    // Shortcuts everywhere (hints, menus, tooltips) are UKbd: square, hairline, monospace.
    kbd: {
      base: 'rounded-none font-mono font-normal',
      variants: {
        size: {
          sm: 'h-4 min-w-4 px-1 text-[10px]',
          md: 'h-5 min-w-5 px-1 text-[11px]'
        }
      },
      defaultVariants: {
        variant: 'outline',
        color: 'neutral'
      }
    },
    popover: {
      slots: {
        content: 'rounded-none bg-transparent ring-0 shadow-none'
      }
    },
    dropdownMenu: {
      slots: {
        content: 'rounded-none bg-elevated ring ring-accented shadow-none p-1 min-w-0',
        item: 'rounded-none text-[12.5px] px-[9px] py-1.5 before:rounded-none',
        separator: 'bg-line my-1 -mx-1'
      }
    },
    modal: {
      slots: {
        content: 'rounded-none bg-elevated ring ring-accented shadow-none divide-default'
      }
    }
  }
})
