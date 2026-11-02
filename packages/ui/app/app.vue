<script setup lang="ts">
// The web UI connects as configured (mock or a client URL). In the desktop shell it first checks the sign-in
// with the shell, then asks it for the sidecar's endpoint (lib/desktop.ts).
import { agentEndpoint, authStatus, isDesktop } from '~/lib/desktop'
import { ConnectTransport } from '~/lib/transport'

useSeoMeta({
  title: 'harness',
  description: 'xlsft`s harness: agent sessions, tools and artifacts.'
})

const store = useSessionStore()
const desktop = isDesktop()
const phase = ref<'checking' | 'signin' | 'ready'>(desktop ? 'checking' : 'ready')
const signInError = ref('')

async function connectDesktop() {
  phase.value = 'checking'
  try {
    const status = await authStatus()
    if (!status.signed_in) {
      phase.value = 'signin'
      return
    }
    const ep = await agentEndpoint()
    await store.init(new ConnectTransport(ep.url, ep.token))
    phase.value = 'ready'
  } catch (err) {
    signInError.value = err instanceof Error ? err.message : String(err)
    phase.value = 'signin'
  }
}

if (desktop) onMounted(connectDesktop)
else await store.init()
</script>

<template>
  <UApp :tooltip="{ delayDuration: 500 }">
    <DesktopSignIn
      v-if="phase === 'signin'"
      :error="signInError"
      @signed-in="connectDesktop"
    />
    <div
      v-else-if="phase === 'checking'"
      class="flex h-svh items-center justify-center bg-default text-[12.5px] text-muted"
    >
      Starting harness…
    </div>
    <NuxtPage v-else />
  </UApp>
</template>
