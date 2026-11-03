<script setup lang="ts">
// Session view (Main.dc.html and the Main* screens): sidebar · header + transcript + composer · right panes (several can be open side by side).
import { SplitterGroup, SplitterPanel } from 'reka-ui'
import { MAIN_SESSION } from '~/lib/transport/scenario'
import { PANE_WIDTH } from '~/composables/usePanes'
import { panesOf } from '~/composables/usePaneLayout'

const store = useSessionStore()
const route = useRoute()
const { panes } = usePanes()
const { tree, width: paneAreaWidth } = usePaneLayout()

// The pane area reports its width so the layout can hide session details or stack new panes when tight.
const paneArea = ref<HTMLElement | null>(null)
let paneObserver: ResizeObserver | undefined
watch(paneArea, (el) => {
  paneObserver?.disconnect()
  if (!el) return
  paneObserver = new ResizeObserver(([e]) => paneAreaWidth.value = Math.round(e!.contentRect.width))
  paneObserver.observe(el)
})
onBeforeUnmount(() => paneObserver?.disconnect())

// Desktop arranges the panes beside the transcript; below lg the last opened pane covers the screen.
const wide = ref(true)
onMounted(() => {
  const mq = window.matchMedia('(min-width: 1024px)')
  wide.value = mq.matches
  mq.addEventListener('change', e => wide.value = e.matches)
})

// Initial share of the pane area: the natural width of the outer row's panes, within 12-70%.
const paneShare = computed(() => {
  if (!tree.value || !import.meta.client) return 40
  const row = tree.value.kind === 'split' && tree.value.dir === 'row' ? tree.value.children : [tree.value]
  const px = row.reduce((s, n) => s + Math.max(...panesOf(n).map(p => PANE_WIDTH[p])), 0)
  return Math.min(70, Math.max(12, (px / Math.max(window.innerWidth - 260, 1)) * 100))
})

const requested = computed(() => typeof route.query.session === 'string' ? route.query.session : '')
// The new-session form: asked for (?new=1), or nothing to show yet on a client without sessions.
const composing = computed(() => route.query.new === '1' || (store.ready && !store.order.length))
watchEffect(() => {
  if (!store.ready || route.query.new === '1') return
  const id = requested.value && store.sessions[requested.value] ? requested.value : store.currentId || (store.sessions[MAIN_SESSION] ? MAIN_SESSION : store.order[0])
  if (id && id !== store.currentId) store.open(id)
})

const session = computed(() => composing.value ? undefined : store.current)
// A session of another machine is read-only here unless the user drives it through the relay.
const readOnly = computed(() => !!session.value?.meta.remote && !(store.remoteControl[session.value.meta.id] && session.value.meta.remoteControllable))
useShortcuts()
</script>

<template>
  <UDashboardGroup
    unit="px"
    storage="local"
    storage-key="harness-layout"
  >
    <AppSidebar />

    <SplitterGroup
      :key="session?.meta.id ?? 'none'"
      direction="horizontal"
      :auto-save-id="`harness-main-split-${session?.meta.id ?? 'none'}`"
      class="h-svh min-w-0 flex-1"
    >
      <SplitterPanel
        id="main"
        :order="0"
        :min-size="10"
        class="@container flex min-h-0 min-w-0 flex-col"
      >
        <SessionHeader
          v-if="session"
          :session="session"
        />
        <template v-if="session">
          <Transcript
            :session="session"
            class="min-h-0 flex-1"
          />
          <div class="shrink-0 px-5 pb-[11px]">
            <div class="mx-auto flex max-w-[790px] flex-col gap-1.5">
              <RemoteNotice
                v-if="readOnly"
                :session="session"
              />
              <Composer
                v-else
                :session="session"
              />
            </div>
          </div>
        </template>
        <NewSession v-else-if="composing" />
        <div
          v-else
          class="flex flex-1 items-center justify-center text-[12.5px] text-muted"
        >
          {{ store.error || 'Connecting to the harness client…' }}
        </div>
      </SplitterPanel>

      <template v-if="session && tree && wide">
        <HSplitHandle dir="row" />
        <SplitterPanel
          id="panes"
          :order="1"
          :default-size="paneShare"
          :min-size="8"
          class="min-h-0 min-w-0"
        >
          <div
            ref="paneArea"
            class="size-full"
          >
            <PaneLayout
              :node="tree"
              :session="session"
            />
          </div>
        </SplitterPanel>
      </template>
    </SplitterGroup>

    <div
      v-if="session && !wide && panes.length"
      class="fixed inset-0 z-40 bg-default"
    >
      <RightPane
        :pane="panes[panes.length - 1]!"
        :session="session"
      />
    </div>
  </UDashboardGroup>
</template>
