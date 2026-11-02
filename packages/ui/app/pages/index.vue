<script setup lang="ts">
// Session view (Main.dc.html and the Main* screens): sidebar · header + transcript + composer · right panes (several can be open side by side).
import { MAIN_SESSION } from '~/lib/transport/scenario'

const store = useSessionStore()
const route = useRoute()
const { panes } = usePanes()

const requested = computed(() => typeof route.query.session === 'string' ? route.query.session : '')
watchEffect(() => {
  if (!store.ready) return
  const id = requested.value && store.sessions[requested.value] ? requested.value : store.currentId || (store.sessions[MAIN_SESSION] ? MAIN_SESSION : store.order[0])
  if (id && id !== store.currentId) store.open(id)
})

const session = computed(() => store.current)
useShortcuts()
</script>

<template>
  <UDashboardGroup
    unit="px"
    storage="local"
    storage-key="harness-layout"
  >
    <AppSidebar />

    <UDashboardPanel
      id="main"
      :ui="{ root: 'min-h-0 h-svh min-w-0 lg:min-w-[420px] flex-[999_1_480px] lg:not-last:border-e-0', body: 'p-0 sm:p-0 gap-0 overflow-hidden' }"
    >
      <template #header>
        <SessionHeader
          v-if="session"
          :session="session"
        />
      </template>
      <template #body>
        <template v-if="session">
          <Transcript
            :session="session"
            class="min-h-0 flex-1"
          />
          <div class="shrink-0 px-5 pb-[11px]">
            <div class="mx-auto flex max-w-[790px] flex-col gap-1.5">
              <Composer :session="session" />
            </div>
          </div>
        </template>
        <div
          v-else
          class="flex flex-1 items-center justify-center text-[12.5px] text-muted"
        >
          {{ store.error || 'Connecting to the harness client…' }}
        </div>
      </template>
    </UDashboardPanel>

    <template v-if="session">
      <RightPane
        v-for="p in panes"
        :key="p"
        :pane="p"
        :session="session"
      />
    </template>
  </UDashboardGroup>
</template>
