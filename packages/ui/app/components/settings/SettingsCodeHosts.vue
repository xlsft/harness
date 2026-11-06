<script setup lang="ts">
// Settings > Code hosts: what this machine and this account can reach GitHub and GitLab with. The local CLIs (device-local),
// the user's accounts on the server's connections, and for admins the connections themselves. Secrets are write-only: they
// go to the server and never come back here.
import { openExternal } from '~/lib/desktop'
import type { CliView, ConnectionView, UserConnectionView } from '~/lib/vcs/types'

const store = useSessionStore()

const clis = ref<CliView[]>([])
const accounts = ref<UserConnectionView[]>([])
const connections = ref<ConnectionView[]>([])
const isAdmin = ref(false)
const loaded = ref(false)
const error = ref('')
const busy = ref('')

const gitlab = reactive({ open: false, name: '', baseUrl: 'https://gitlab.com', clientId: '', clientSecret: '', webhookSecret: '', accessToken: '', allowPrivateNetwork: false })
const github = reactive({ open: false, baseUrl: 'https://github.com', organization: '', appName: '' })

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

const api = () => store.transport!.vcs()

async function refresh() {
  if (!store.transport) return
  error.value = ''
  try {
    const [c, a] = await Promise.all([api().clis(), api().userConnections()])
    clis.value = c
    accounts.value = a
    isAdmin.value = await store.transport.users().isAdmin().catch(() => false)
    connections.value = isAdmin.value ? await api().connections() : []
  } catch (e) {
    error.value = message(e)
  } finally {
    loaded.value = true
  }
}
onMounted(refresh)

let polling = true
onBeforeUnmount(() => {
  polling = false
})

// The user finishes signing in in the browser; this waits for the server to say how it went.
async function connect(a: UserConnectionView) {
  busy.value = a.id
  error.value = ''
  try {
    const start = await api().startAuth(a.id)
    await openExternal(start.authorizationUrl)
    const deadline = Date.now() + 5 * 60_000
    while (polling && Date.now() < deadline) {
      const s = await api().authStatus(a.id, start.flowId)
      if (s.state === 'connected') break
      if (s.state === 'error') throw new Error(s.error || 'The authorization failed')
      await new Promise(r => setTimeout(r, 1500))
    }
    await refresh()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = ''
  }
}

async function disconnect(a: UserConnectionView) {
  busy.value = a.id
  try {
    await api().revokeAuth(a.id)
    await refresh()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = ''
  }
}

// GitHub takes the app manifest as a form the admin's browser posts; it then comes back to the server's callback, which
// stores the connection. Refresh shows it.
async function registerGitHubApp() {
  error.value = ''
  try {
    const m = await api().gitHubManifest({ baseUrl: github.baseUrl.trim(), organization: github.organization.trim(), appName: github.appName.trim() })
    const form = document.createElement('form')
    form.method = 'POST'
    form.action = m.postUrl
    form.target = '_blank'
    const field = document.createElement('input')
    field.type = 'hidden'
    field.name = 'manifest'
    field.value = m.manifest
    form.append(field)
    document.body.append(form)
    form.submit()
    form.remove()
    github.open = false
  } catch (e) {
    error.value = message(e)
  }
}

async function saveGitLab() {
  error.value = ''
  try {
    const opt = (s: string) => s.trim() || undefined
    await api().putGitLab({ name: gitlab.name.trim(), baseUrl: gitlab.baseUrl.trim(), clientId: opt(gitlab.clientId), clientSecret: opt(gitlab.clientSecret), webhookSecret: opt(gitlab.webhookSecret), accessToken: opt(gitlab.accessToken), allowPrivateNetwork: gitlab.allowPrivateNetwork })
    Object.assign(gitlab, { open: false, name: '', clientId: '', clientSecret: '', webhookSecret: '', accessToken: '', allowPrivateNetwork: false })
    await refresh()
  } catch (e) {
    error.value = message(e)
  }
}

async function removeConnection(c: ConnectionView) {
  error.value = ''
  try {
    await api().deleteConnection(c.id)
    await refresh()
  } catch (e) {
    error.value = message(e)
  }
}

const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <section
    aria-label="Code hosts"
    class="flex flex-col"
  >
    <h2 class="set-h">
      Code hosts
    </h2>
    <p class="set-sub">
      What opens pull requests, reads CI and follows reviews. A server connection goes first; the CLIs on this machine cover the rest.
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <h3 class="mt-2 mb-1 text-[12.5px] text-strong">
      Your accounts
    </h3>
    <p
      v-if="loaded && !accounts.length"
      class="m-0 text-[12px] text-muted"
    >
      The server has no code host connection yet.{{ isAdmin ? ' Add one below.' : ' An admin can add one.' }}
    </p>
    <div
      v-for="a in accounts"
      :key="a.id"
      class="set-row"
    >
      <span class="set-label">
        <span>{{ a.name || a.host }}</span>
        <span class="set-desc">
          <template v-if="a.state === 'connected'">Connected as {{ a.login }}</template>
          <template v-else-if="a.canWrite">Acts through the connection's access token</template>
          <template v-else>Not connected</template>
          ({{ a.host }})
        </span>
      </span>
      <HBtn
        v-if="a.state === 'connected'"
        size="sm"
        tone="muted"
        :disabled="busy === a.id"
        @click="disconnect(a)"
      >
        Disconnect
      </HBtn>
      <HBtn
        v-else
        size="sm"
        :disabled="busy === a.id"
        @click="connect(a)"
      >
        {{ busy === a.id ? 'Waiting for the browser' : 'Connect' }}
      </HBtn>
    </div>

    <h3 class="mt-4 mb-1 text-[12.5px] text-strong">
      On this machine
    </h3>
    <div
      v-for="c in clis"
      :key="c.name"
      class="set-row"
    >
      <span class="set-label">
        <span>{{ c.name }}</span>
        <span class="set-desc">
          <template v-if="c.problem">{{ c.problem }}</template>
          <template v-else-if="!c.installed">Not installed</template>
          <template v-else-if="!c.hosts.length">Installed, not signed in. Run <code>{{ c.loginCommand }}</code></template>
          <template v-else>{{ c.hosts.map(h => `${h.login} on ${h.host}`).join(', ') }}</template>
        </span>
      </span>
    </div>

    <template v-if="isAdmin">
      <h3 class="mt-4 mb-1 text-[12.5px] text-strong">
        Server connections
      </h3>
      <p class="m-0 mb-2 text-[12px] text-muted">
        Everyone on this server can use these. Keys are stored on the server and never shown again.
      </p>
      <div
        v-for="c in connections"
        :key="c.id"
        class="set-row"
      >
        <span class="set-label">
          <span>{{ c.name || c.baseUrl }}</span>
          <span class="set-desc">
            {{ c.baseUrl }}: {{ c.healthy ? 'healthy' : (c.lastError || 'not healthy') }}; webhooks {{ c.webhooksOk ? 'arriving' : 'not seen lately' }}
          </span>
        </span>
        <HBtn
          v-if="c.installUrl"
          size="sm"
          @click="openExternal(c.installUrl)"
        >
          Install
        </HBtn>
        <HBtn
          size="sm"
          tone="danger"
          :aria-label="`Remove ${c.name || c.baseUrl}`"
          @click="removeConnection(c)"
        >
          Remove
        </HBtn>
      </div>
      <div class="mt-2 flex gap-2">
        <HBtn
          size="sm"
          @click="github.open = !github.open"
        >
          Add GitHub App
        </HBtn>
        <HBtn
          size="sm"
          @click="gitlab.open = !gitlab.open"
        >
          Add GitLab
        </HBtn>
        <HBtn
          size="sm"
          tone="muted"
          @click="refresh"
        >
          Refresh
        </HBtn>
      </div>

      <form
        v-if="github.open"
        aria-label="Register a GitHub App"
        class="mt-3 flex flex-col gap-2"
        @submit.prevent="registerGitHubApp"
      >
        <input
          v-model="github.baseUrl"
          aria-label="GitHub URL"
          placeholder="https://github.com"
          :class="inputClass"
        >
        <input
          v-model="github.organization"
          aria-label="Organization"
          placeholder="Organization (empty: your own account)"
          :class="inputClass"
        >
        <input
          v-model="github.appName"
          aria-label="App name"
          placeholder="App name"
          :class="inputClass"
        >
        <p class="m-0 text-[12px] text-muted">
          GitHub opens in a new tab to create the app; then install it, and Refresh here.
        </p>
        <HBtn
          type="submit"
          variant="primary"
          size="md"
          class="self-start"
        >
          Register on GitHub
        </HBtn>
      </form>

      <form
        v-if="gitlab.open"
        aria-label="Connect GitLab"
        class="mt-3 flex flex-col gap-2"
        @submit.prevent="saveGitLab"
      >
        <input
          v-model="gitlab.name"
          aria-label="Name"
          placeholder="Name"
          :class="inputClass"
        >
        <input
          v-model="gitlab.baseUrl"
          aria-label="GitLab URL"
          placeholder="https://gitlab.com"
          :class="inputClass"
        >
        <input
          v-model="gitlab.clientId"
          aria-label="OAuth application id"
          placeholder="OAuth application id"
          autocomplete="off"
          :class="inputClass"
        >
        <input
          v-model="gitlab.clientSecret"
          aria-label="OAuth application secret"
          type="password"
          placeholder="OAuth application secret"
          autocomplete="off"
          :class="inputClass"
        >
        <input
          v-model="gitlab.accessToken"
          aria-label="Group or project access token"
          type="password"
          placeholder="Group or project access token (optional)"
          autocomplete="off"
          :class="inputClass"
        >
        <input
          v-model="gitlab.webhookSecret"
          aria-label="Webhook secret"
          type="password"
          placeholder="Webhook secret (optional)"
          autocomplete="off"
          :class="inputClass"
        >
        <div class="flex items-center gap-2 text-[12px] text-muted">
          <HToggle
            v-model="gitlab.allowPrivateNetwork"
            label="On a private network"
          /> On a private network
        </div>
        <HBtn
          type="submit"
          variant="primary"
          size="md"
          class="self-start"
          :disabled="!gitlab.baseUrl.trim()"
        >
          Connect
        </HBtn>
      </form>
    </template>
  </section>
</template>
