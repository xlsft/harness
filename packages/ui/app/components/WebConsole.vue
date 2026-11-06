<script setup lang="ts">
// The web UI as the server serves it (transport "web"), before a client is connected. Agent work runs on a client the user runs:
// the server never executes anything, so this page says so and connects to one with its address and token. What needs no client
// works from here with the browser's session: the account and, by role, the server's administration.
import { saveClientEndpoint } from '~/lib/web/endpoint'
import { ConnectTransport } from '~/lib/transport'

const props = defineProps<{ error?: string }>()
const store = useSessionStore()
const access = useAccess()

const checking = ref(true)
const user = ref<{ name: string, email: string } | null>(null)
const url = ref('http://127.0.0.1:7801')
const token = ref('')
const origin = window.location.origin

const command = computed(() => `harness-client serve --listen 127.0.0.1:${portOf(url.value)} --cors-origin ${origin}`)

function portOf(u: string) {
  try {
    return new URL(u).port || '7801'
  } catch {
    return '7801'
  }
}

onMounted(async () => {
  try {
    const r = await fetch('/api/auth/get-session', { headers: { accept: 'application/json' } })
    const s = r.ok ? await r.json() as { user?: { name: string, email: string }, session?: { mfaPending?: boolean } } | null : null
    if (s?.session?.mfaPending) {
      window.location.href = '/device?return=/'
      return
    }
    if (s?.user) {
      user.value = s.user
      // The same screens as in the app, talking to this server directly: everything they call takes the session cookie.
      store.transport = markRaw(new ConnectTransport(origin))
      await access.load(true)
    }
  } finally {
    checking.value = false
  }
})

function connect() {
  saveClientEndpoint(url.value.trim(), token.value.trim())
  window.location.href = '/'
}

async function signOut() {
  await fetch('/api/auth/sign-out', { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' })
  window.location.href = '/'
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div class="flex h-svh min-h-0 flex-col bg-default">
    <header class="flex h-12 shrink-0 items-center gap-3 border-b border-line px-5">
      <HWordmark class="h-[18px] w-auto text-strong" />
      <span class="flex-1" />
      <template v-if="user">
        <span class="truncate text-[12px] text-muted">{{ user.email }}</span>
        <HBtn
          size="sm"
          @click="signOut"
        >
          Sign out
        </HBtn>
      </template>
    </header>

    <HLoader v-if="checking" />

    <div
      v-else
      class="min-h-0 flex-1 overflow-y-auto"
    >
      <div class="mx-auto flex w-full max-w-[920px] flex-col gap-6 px-5 py-6">
        <section
          aria-label="Connect a client"
          class="flex flex-col gap-3 border border-default p-4"
        >
          <h1 class="m-0 text-[15px] font-semibold text-strong">
            Connect a client to work with agents
          </h1>
          <p class="m-0 text-[12.5px] leading-[1.6] text-toned">
            The server never runs code: sessions, tools and files live on a client on your machine, or one you can reach. Start it with this page's address allowed, then give this tab its address and token. The page talks to the client from your browser; the token stays in this tab.
          </p>
          <code class="border border-default bg-raised px-2 py-1.5 font-mono text-[12px] break-all text-strong select-all">{{ command }}</code>
          <p
            v-if="props.error"
            role="alert"
            class="m-0 border border-bad px-3 py-2 text-[12px] text-bad"
          >
            Could not reach that client: {{ props.error.replace(/^\[\w+\]\s*/, '') }}. Is it running, with this page's address allowed?
          </p>
          <form
            class="grid grid-cols-[1fr_1fr_auto] items-end gap-2 max-sm:grid-cols-1"
            @submit.prevent="connect"
          >
            <label class="flex flex-col gap-1 text-[11.5px] text-muted">Client address
              <input
                v-model="url"
                required
                spellcheck="false"
                autocomplete="off"
                :class="input"
              >
            </label>
            <label class="flex flex-col gap-1 text-[11.5px] text-muted">Token (it prints it at start)
              <input
                v-model="token"
                type="password"
                required
                autocomplete="off"
                :class="input"
              >
            </label>
            <HBtn
              size="md"
              variant="primary"
              type="submit"
            >
              Connect
            </HBtn>
          </form>
        </section>

        <template v-if="user">
          <section
            aria-label="Without a client"
            class="flex min-h-[560px] flex-col overflow-hidden border border-default"
          >
            <h2 class="m-0 border-b border-line px-4 py-2.5 text-[13px] font-semibold text-strong">
              Without a client: your account and this server's administration
            </h2>
            <SettingsPage server-only />
          </section>
        </template>
        <section
          v-else
          class="flex flex-col items-start gap-3 border border-default p-4"
        >
          <h2 class="m-0 text-[15px] font-semibold text-strong">
            Sign in for what needs no client
          </h2>
          <p class="m-0 text-[12.5px] leading-[1.6] text-toned">
            Your account, two-factor sign-in, your usage and, depending on your role, users, single sign-on, the audit log, quotas and policy.
          </p>
          <a
            href="/device?return=/"
            class="flex h-8 items-center bg-accent px-3 text-[12.5px] text-on-accent no-underline"
          >Sign in</a>
        </section>
      </div>
    </div>
  </div>
</template>
