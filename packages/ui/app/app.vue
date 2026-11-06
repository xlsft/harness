<script setup lang="ts">
// The web UI connects as configured (mock or a client URL). In the desktop shell it first checks the sign-in
// with the shell, then asks it for the sidecar's endpoint (lib/desktop.ts).
import { agentEndpoint, authStatus, guardQuit, isDesktop } from '~/lib/desktop'
import { ConnectTransport } from '~/lib/transport'

useSeoMeta({
  title: 'xlsft`s harness',
  description: 'xlsft`s harness: agent sessions, tools and artifacts.'
})

const store = useSessionStore()
const desktop = isDesktop()
useTurnNotifications()
useUserNotifications()
useDeviceSettings()
useScreenReader().start()
useHead({ htmlAttrs: { lang: currentLocale() } })
const phase = ref<'checking' | 'signin' | 'ready'>(desktop ? 'checking' : 'ready')

// Quitting the desktop app warns while work is still running here: sessions in a turn, background tasks, agents.
const { runningWork } = useRunningWork()
if (desktop) guardQuit(() => runningWork('Quitting stops them.'))
if (desktop) startAppUpdateChecks()
const signInError = ref('')

async function connectDesktop() {
  phase.value = 'checking'
  try {
    const status = await authStatus()
    if (!status.signed_in) {
      phase.value = 'signin'
      return
    }
    store.account = { name: status.name ?? '', server: status.server ?? '', role: status.role ?? '' }
    const ep = await agentEndpoint()
    await store.init(new ConnectTransport(ep.url, ep.token))
    endpointUrl = ep.url
    phase.value = 'ready'
  } catch (err) {
    signInError.value = err instanceof Error ? err.message : String(err)
    phase.value = 'signin'
  }
}

// Desktop: the sidecar can go away (app restart in dev, crash). When the store sees a network error, or the
// periodic ping finds the endpoint gone, reload: the shell starts a new sidecar and sessions resume from history.
let endpointUrl = ''
let reloading = false
function reconnect() {
  if (reloading) return
  reloading = true
  window.location.reload()
}

if (desktop) {
  onMounted(async () => {
    await connectDesktop()
    const timer = setInterval(async () => {
      if (!endpointUrl) return
      try {
        await fetch(endpointUrl, { mode: 'no-cors', cache: 'no-store' })
      } catch {
        reconnect()
      }
    }, 5000)
    onBeforeUnmount(() => clearInterval(timer))
  })
  watch(() => store.connectionLost, lost => lost && reconnect())
} else {
  await store.init()
}
</script>

<template>
  <UApp :tooltip="{ delayDuration: 500 }">
    <DesktopSignIn
      v-if="phase === 'signin'"
      :error="signInError"
      @signed-in="connectDesktop"
    />
    <HLoader v-else-if="phase === 'checking'" />
    <template v-else>
      <NuxtPage />
      <ScreenReaderRegions />
      <SshHostBar />
      <SettingsModal />
      <SearchModal />
    </template>
  </UApp>
</template>
