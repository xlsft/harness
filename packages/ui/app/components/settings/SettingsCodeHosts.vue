<script setup lang="ts">
// Settings > VCS: what this machine and this account can reach GitHub and GitLab with. The local CLIs (device-local),
// the user's accounts on the server's connections, and for admins the connections themselves. Secrets are write-only: they
// go to the server and never come back here.
import { openExternal } from '~/lib/desktop'
import type { CliView, ConnectionView, UserConnectionView } from '~/lib/vcs/types'

const store = useSessionStore()

const clis = ref<CliView[]>([])
const accounts = ref<UserConnectionView[]>([])
const connections = ref<ConnectionView[]>([])
const isAdmin = ref(false)
const error = ref('')
const busy = ref('')

const gitlab = reactive({ open: false, name: '', baseUrl: 'https://gitlab.com', clientId: '', clientSecret: '', webhookSecret: '', accessToken: '', allowPrivateNetwork: false })
const github = reactive({ open: false, baseUrl: 'https://github.com', organization: '', appName: '' })

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

const api = () => store.transport!.vcs()

// Each part loads on its own and shows up as soon as it has: the local CLIs can take seconds (glab asks every GitLab host
// it knows), and the server's accounts never wait for them.
const clisLoaded = ref(false)
const accountsLoaded = ref(false)

async function loadClis() {
  try {
    clis.value = await api().clis()
  } catch (e) {
    error.value = message(e)
  } finally {
    clisLoaded.value = true
  }
}

async function loadAccounts() {
  try {
    const [a, admin] = await Promise.all([api().userConnections(), store.transport!.users().isAdmin().catch(() => false)])
    accounts.value = a
    isAdmin.value = admin
    connections.value = admin ? await api().connections() : []
  } catch (e) {
    error.value = message(e)
  } finally {
    accountsLoaded.value = true
  }
}

async function refresh() {
  if (!store.transport) return
  error.value = ''
  await Promise.all([loadClis(), loadAccounts()])
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

// Tile of a code host or CLI, like the provider tiles in Settings > Models: its logo (utils/providerLogos.ts).
const MONO: Record<string, string> = { github: 'github', gitlab: 'gitlab', gh: 'github', glab: 'gitlab' }
const mono = (k: string) => MONO[k] ?? k.slice(0, 2).toUpperCase()
const GITHUB_APP_DOCS = 'https://docs.github.com/en/apps/creating-github-apps'
const GITLAB_OAUTH_DOCS = 'https://docs.gitlab.com/integration/oauth_provider/'

const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <section
    :aria-label="tt('VCS')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('VCS') }}
    </h2>
    <p class="set-sub">
      {{ tt('What opens pull requests, reads CI and follows reviews. A server connection goes first; the CLIs on this machine cover the rest.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <section
      :aria-label="tt('Your accounts')"
      class="mb-8 flex flex-col"
    >
      <h3 class="m-0 text-[13px] font-semibold text-strong">
        {{ tt('Your accounts') }}
      </h3>
      <p class="m-0 mt-1 mb-3 text-[12px] text-muted">
        {{ tt('You on the code hosts this server is connected to. Pull requests, comments and reviews go out under your name.') }}
      </p>
      <div class="overflow-hidden border border-default">
        <div
          v-if="!accountsLoaded || !accounts.length"
          class="px-3 py-2.5 text-[12px] text-muted"
        >
          <template v-if="!accountsLoaded">
            {{ tt('Loading…') }}
          </template>
          <template v-else>
            {{ tt('The server has no code host connection yet.{0}', [isAdmin ? tt(' Add one below.') : tt(' An admin can add one.')]) }}
          </template>
        </div>
        <div
          v-for="(a, i) in accounts"
          :key="a.id"
          class="flex items-center gap-3 px-3 py-2.5"
          :class="{ 'border-t border-default': i > 0 }"
        >
          <SettingsMonogram
            :text="mono(a.kind)"
            :dim="a.state !== 'connected'"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ a.name || a.host }}
              <span
                v-if="a.state === 'connected'"
                class="text-[11px] text-ok"
              >{{ tt('connected') }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">
              <template v-if="a.state === 'connected'">{{ tt('Signed in as {0} on {1}', [a.login, a.host]) }}</template>
              <template v-else-if="a.canWrite">{{ tt('Acts through the connection\'s access token on {0}', [a.host]) }}</template>
              <template v-else>{{ tt('Not connected to {0}', [a.host]) }}</template>
            </span>
          </span>
          <HBtn
            v-if="a.state === 'connected'"
            size="sm"
            tone="muted"
            :disabled="busy === a.id"
            @click="disconnect(a)"
          >
            {{ tt('Disconnect') }}
          </HBtn>
          <HBtn
            v-else
            size="sm"
            :disabled="busy === a.id"
            @click="connect(a)"
          >
            {{ busy === a.id ? tt('Waiting for the browser') : tt('Connect') }}
          </HBtn>
        </div>
      </div>
    </section>

    <section
      :aria-label="tt('On this machine')"
      class="mb-8 flex flex-col"
    >
      <h3 class="m-0 text-[13px] font-semibold text-strong">
        {{ tt('On this machine') }}
      </h3>
      <p class="m-0 mt-1 mb-3 text-[12px] text-muted">
        {{ tt('The gh and glab command-line tools here and the accounts they are signed in to. Used for hosts the server has no connection to.') }}
      </p>
      <div class="overflow-hidden border border-default">
        <div
          v-if="!clisLoaded"
          class="px-3 py-2.5 text-[12px] text-muted"
        >
          {{ tt('Checking gh and glab…') }}
        </div>
        <div
          v-for="(c, i) in clis"
          :key="c.name"
          class="flex items-center gap-3 px-3 py-2.5"
          :class="{ 'border-t border-default': i > 0 }"
        >
          <SettingsMonogram
            :text="mono(c.name)"
            :dim="!c.hosts.length"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ c.name }}
              <span
                v-if="c.version"
                class="text-[11px] text-dimmed"
              >{{ c.version }}</span>
              <span
                v-if="c.hosts.length && !c.problem"
                class="text-[11px] text-ok"
              >{{ tt('signed in') }}</span>
            </span>
            <span
              class="text-[11.5px]"
              :class="c.problem ? 'text-warn' : 'truncate text-muted'"
            >
              <template v-if="c.problem">{{ c.problem }}</template>
              <template v-else-if="!c.installed">{{ tt('Not installed') }}</template>
              <template v-else-if="!c.hosts.length">{{ tt('Installed, not signed in. Run') }} <code>{{ c.loginCommand }}</code></template>
              <template v-else>{{ c.hosts.map(h => `${h.login} on ${h.host}`).join(', ') }}</template>
            </span>
          </span>
        </div>
      </div>
    </section>

    <section
      v-if="isAdmin"
      :aria-label="tt('Server connections')"
      class="mb-8 flex flex-col"
    >
      <h3 class="m-0 text-[13px] font-semibold text-strong">
        {{ tt('Server connections') }}
      </h3>
      <p class="m-0 mt-1 mb-3 text-[12px] text-muted">
        {{ tt('Everyone on this server can use these. Keys are stored on the server and never shown again.') }}
      </p>
      <div class="overflow-hidden border border-default">
        <div
          v-for="c in connections"
          :key="c.id"
          class="flex items-center gap-3 border-b border-default px-3 py-2.5"
        >
          <SettingsMonogram :text="mono(c.kind)" />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ c.name || c.baseUrl }}
              <span
                class="text-[11px]"
                :class="c.healthy ? 'text-ok' : 'text-bad'"
              >{{ c.healthy ? tt('healthy') : tt('not healthy') }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">
              {{ tt('{0}; webhooks {1}', [c.healthy ? c.baseUrl : (c.lastError || c.baseUrl), c.webhooksOk ? tt('arriving') : tt('not seen lately')]) }}
            </span>
          </span>
          <HBtn
            v-if="c.installUrl"
            size="sm"
            @click="openExternal(c.installUrl)"
          >
            {{ tt('Install') }}
          </HBtn>
          <HBtn
            size="sm"
            tone="danger"
            :aria-label="tt('Remove {0}', [c.name || c.baseUrl])"
            @click="removeConnection(c)"
          >
            {{ tt('Remove') }}
          </HBtn>
        </div>

        <div class="flex flex-col">
          <div class="flex items-center gap-3 px-3 py-2.5">
            <SettingsMonogram
              text="github"
              dim
            />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="text-[12.5px] text-strong">{{ tt('GitHub App') }}</span>
              <span class="truncate text-[11.5px] text-muted">{{ tt('GitHub or GitHub Enterprise, through an app registered for your account or organization.') }}</span>
            </span>
            <SettingsLink
              :href="GITHUB_APP_DOCS"
              class="max-sm:hidden"
            >
              {{ tt('Docs') }}
            </SettingsLink>
            <HBtn
              v-if="!github.open"
              size="sm"
              @click="github.open = true"
            >
              {{ tt('Connect') }}
            </HBtn>
          </div>
          <form
            v-if="github.open"
            :aria-label="tt('Register a GitHub App')"
            class="flex flex-col gap-2 px-3 pb-3 pl-[52px]"
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
              :aria-label="tt('Organization')"
              :placeholder="tt('Organization (empty: your own account)')"
              :class="inputClass"
            >
            <input
              v-model="github.appName"
              :aria-label="tt('App name')"
              :placeholder="tt('App name')"
              :class="inputClass"
            >
            <p class="m-0 text-[12px] text-muted">
              {{ tt('GitHub opens in a new tab to create the app; then install it, and Refresh here.') }}
            </p>
            <div class="flex gap-2">
              <HBtn
                type="submit"
                variant="primary"
                size="sm"
              >
                {{ tt('Register on GitHub') }}
              </HBtn>
              <HBtn
                size="sm"
                tone="muted"
                @click="github.open = false"
              >
                {{ tt('Cancel') }}
              </HBtn>
            </div>
          </form>
        </div>

        <div class="flex flex-col border-t border-default">
          <div class="flex items-center gap-3 px-3 py-2.5">
            <SettingsMonogram
              text="gitlab"
              dim
            />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="text-[12.5px] text-strong">{{ tt('GitLab') }}</span>
              <span class="truncate text-[11.5px] text-muted">{{ tt('GitLab.com or your own instance, with an OAuth application and an optional access token.') }}</span>
            </span>
            <SettingsLink
              :href="GITLAB_OAUTH_DOCS"
              class="max-sm:hidden"
            >
              {{ tt('Docs') }}
            </SettingsLink>
            <HBtn
              v-if="!gitlab.open"
              size="sm"
              @click="gitlab.open = true"
            >
              {{ tt('Connect') }}
            </HBtn>
          </div>
          <form
            v-if="gitlab.open"
            :aria-label="tt('Connect GitLab')"
            class="flex flex-col gap-2 px-3 pb-3 pl-[52px]"
            @submit.prevent="saveGitLab"
          >
            <input
              v-model="gitlab.name"
              :aria-label="tt('Name')"
              :placeholder="tt('Name')"
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
              :aria-label="tt('OAuth application id')"
              :placeholder="tt('OAuth application id')"
              autocomplete="off"
              :class="inputClass"
            >
            <input
              v-model="gitlab.clientSecret"
              :aria-label="tt('OAuth application secret')"
              type="password"
              :placeholder="tt('OAuth application secret')"
              autocomplete="off"
              :class="inputClass"
            >
            <input
              v-model="gitlab.accessToken"
              :aria-label="tt('Group or project access token')"
              type="password"
              :placeholder="tt('Group or project access token (optional)')"
              autocomplete="off"
              :class="inputClass"
            >
            <input
              v-model="gitlab.webhookSecret"
              :aria-label="tt('Webhook secret')"
              type="password"
              :placeholder="tt('Webhook secret (optional)')"
              autocomplete="off"
              :class="inputClass"
            >
            <div class="flex items-center gap-2 text-[12px] text-muted">
              <HToggle
                v-model="gitlab.allowPrivateNetwork"
                :label="tt('On a private network')"
              /> {{ tt('On a private network') }}
            </div>
            <div class="flex gap-2">
              <HBtn
                type="submit"
                variant="primary"
                size="sm"
                :disabled="!gitlab.baseUrl.trim()"
              >
                {{ tt('Connect') }}
              </HBtn>
              <HBtn
                size="sm"
                tone="muted"
                @click="gitlab.open = false"
              >
                {{ tt('Cancel') }}
              </HBtn>
            </div>
          </form>
        </div>
      </div>
      <HBtn
        size="sm"
        tone="muted"
        class="mt-2 self-start"
        @click="refresh"
      >
        {{ tt('Refresh') }}
      </HBtn>
    </section>
  </section>
</template>
