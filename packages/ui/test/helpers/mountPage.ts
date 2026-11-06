// Mounts the real Extensions page without Nuxt: its auto-imports become globals, its components are the real ones, and only the Nuxt UI
// popover (a portal) and the router are stand-ins.
import { flushPromises, mount } from '@vue/test-utils'
import * as vue from 'vue'
import { defineComponent, h, reactive, ref } from 'vue'
import { vi } from 'vitest'

export function stubNuxtGlobals(services: { transport: unknown, query: Record<string, string>, openSettings: (s: string) => void }) {
  for (const name of ['ref', 'computed', 'reactive', 'watch', 'onMounted', 'onBeforeUnmount', 'defineModel', 'nextTick', 'toRef', 'watchEffect'] as const) {
    vi.stubGlobal(name, (vue as Record<string, unknown>)[name])
  }
  const route = reactive({ query: services.query })
  vi.stubGlobal('useRoute', () => route)
  vi.stubGlobal('useRouter', () => ({
    replace: async (to: { query: Record<string, string> }) => {
      route.query = to.query
    },
    push: async (to: { query: Record<string, string> }) => {
      route.query = to.query
    }
  }))
  vi.stubGlobal('useSessionStore', () => ({ transport: services.transport, current: null }))
  vi.stubGlobal('useSettingsModal', () => ({ open: services.openSettings }))
  vi.stubGlobal('useToast', () => ({ add: () => {} }))
  return route
}

// Popovers show their content once their trigger is clicked, in place.
const PopoverStub = defineComponent({
  props: { open: { type: Boolean, default: undefined } },
  emits: ['update:open'],
  setup(props, { slots, emit }) {
    const inner = ref(false)
    return () => {
      const shown = props.open ?? inner.value
      return h('div', {
        onClick: (e: Event) => {
          if ((e.target as Element).closest('[role=menu]')) return
          inner.value = !shown
          emit('update:open', !shown)
        }
      }, [slots.default?.({ open: shown }), shown ? slots.content?.() : null])
    }
  }
})

const components = import.meta.glob('../../app/components/**/{Extensions*,RowMenu,PaneMenuItem,HBtn,HIcon,HToggle,HSelect,HKeys,HLoader,SettingsLink}.vue')
let registered: Record<string, unknown> | undefined

export async function loadComponents() {
  if (registered) return registered
  const out: Record<string, unknown> = {}
  for (const [path, load] of Object.entries(components)) {
    out[path.split('/').pop()!.replace('.vue', '')] = ((await load()) as { default: unknown }).default
  }
  registered = out
  return out
}

export async function mountExtensions() {
  const registered = await loadComponents()
  const page = registered.ExtensionsPage as never
  const wrapper = mount(page, {
    attachTo: document.body,
    global: { components: { ...registered, UPopover: PopoverStub } as never, stubs: { teleport: true } }
  })
  await settle()
  return wrapper
}

export async function settle() {
  for (let i = 0; i < 5; i++) await flushPromises()
}
