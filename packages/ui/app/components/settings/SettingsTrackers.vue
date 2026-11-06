<script setup lang="ts">
// Settings > Trackers: external trackers as providers. Personal connections stay on this device, with their credentials in the
// system keychain; company ones are the server's, for admins to manage. Secrets are write-only and never come back here.
import { openExternal } from '~/lib/desktop'
import { errorText } from '~/lib/library/types'
import { AUTH_LABELS, TrackerAuthMethod, TrackerConnectionScope, type TrackerCapabilities, type TrackerConnection } from '~/lib/issues/types'

const store = useSessionStore()
const api = () => store.transport!.issues()
const connections = ref<TrackerConnection[]>([])
const providers = ref<TrackerCapabilities[]>([])
const isAdmin = ref(false)
const error = ref('')
const notes = ref<Record<string, string>>({})
const confirming = ref('')

const blank = () => ({ open: false, id: '', scope: TrackerConnectionScope.DEVICE, provider: 'youtrack', name: '', baseUrl: '', auth: TrackerAuthMethod.TOKEN, login: '', secret: '', oauthClientId: '', oauthClientSecret: '', enabled: true, allowPrivateNetwork: false, knowledgeEnabled: false, knowledgeQuery: '' })
const form = reactive(blank())

async function refresh() {
  if (!store.transport) return
  try {
    const r = await api().listConnections()
    connections.value = r.connections
    providers.value = r.providers
    isAdmin.value = await store.transport.users().isAdmin().catch(() => false)
    error.value = ''
  } catch (e) {
    error.value = errorText(e)
  }
}
onMounted(refresh)
watch(() => store.transport, refresh)

const device = computed(() => connections.value.filter(c => c.scope === TrackerConnectionScope.DEVICE))
const company = computed(() => connections.value.filter(c => c.scope === TrackerConnectionScope.ADMIN))
const capability = computed(() => providers.value.find(p => p.provider === form.provider))
const authOptions = computed(() => (capability.value?.authMethods ?? []).map(m => ({ value: String(m), label: AUTH_LABELS[m] ?? '' })))
const providerOptions = computed(() => providers.value.map(p => ({ value: p.provider, label: p.displayName })))
const needsSecret = computed(() => form.auth !== TrackerAuthMethod.OAUTH)

function add(scope: TrackerConnectionScope) {
  Object.assign(form, blank(), { open: true, scope })
}

function edit(c: TrackerConnection) {
  Object.assign(form, blank(), { open: true, id: c.id, scope: c.scope, provider: c.provider, name: c.name, baseUrl: c.baseUrl, auth: c.auth, login: c.login, oauthClientId: c.oauthClientId, enabled: c.enabled, allowPrivateNetwork: c.allowPrivateNetwork, knowledgeEnabled: c.knowledgeEnabled, knowledgeQuery: c.knowledgeQuery })
}

function input() {
  return {
    id: form.id,
    provider: form.provider,
    name: form.name.trim(),
    baseUrl: form.baseUrl.trim(),
    auth: form.auth,
    login: form.login.trim(),
    oauthClientId: form.oauthClientId.trim(),
    secret: form.secret || undefined,
    oauthClientSecret: form.oauthClientSecret || undefined,
    enabled: form.enabled,
    allowPrivateNetwork: form.allowPrivateNetwork,
    knowledgeEnabled: form.knowledgeEnabled,
    knowledgeQuery: form.knowledgeQuery.trim()
  }
}

async function save() {
  try {
    await api().saveConnection(form.scope, input())
    form.open = false
    await refresh()
  } catch (e) {
    error.value = errorText(e)
  }
}

const draftNote = ref('')
async function testDraft() {
  draftNote.value = tt('Testing…')
  try {
    const r = form.id && !form.secret ? await api().testConnection(form.scope, { id: form.id }) : await api().testConnection(form.scope, { draft: input() })
    draftNote.value = r.ok ? tt('Signed in as {0}', [r.displayName || r.login]) : r.error
  } catch (e) {
    draftNote.value = errorText(e)
  }
}

async function test(c: TrackerConnection) {
  notes.value[c.id] = tt('Testing…')
  try {
    const r = await api().testConnection(c.scope, { id: c.id })
    notes.value[c.id] = r.ok ? tt('Signed in as {0}', [r.displayName || r.login]) : r.error
    await refresh()
  } catch (e) {
    notes.value[c.id] = errorText(e)
  }
}

async function setEnabled(c: TrackerConnection, enabled: boolean) {
  try {
    await api().saveConnection(c.scope, { id: c.id, provider: c.provider, name: c.name, baseUrl: c.baseUrl, auth: c.auth, login: c.login, oauthClientId: c.oauthClientId, enabled, allowPrivateNetwork: c.allowPrivateNetwork, knowledgeEnabled: c.knowledgeEnabled, knowledgeQuery: c.knowledgeQuery })
    await refresh()
  } catch (e) {
    error.value = errorText(e)
  }
}

async function remove(c: TrackerConnection) {
  if (confirming.value !== c.id) {
    confirming.value = c.id
    return
  }
  try {
    await api().deleteConnection(c.scope, c.id)
    confirming.value = ''
    await refresh()
  } catch (e) {
    error.value = errorText(e)
  }
}

let timer: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(timer))

async function authorize(c: TrackerConnection) {
  try {
    const { url, flowId } = await api().startOAuth(c.scope, c.id)
    await openExternal(url)
    notes.value[c.id] = tt('Waiting for the browser…')
    if (!flowId) return
    const poll = async () => {
      const r = await api().oauthStatus(flowId)
      if (!r.done) {
        timer = setTimeout(poll, 1500)
        return
      }
      notes.value[c.id] = r.error || tt('Authorized')
      await refresh()
    }
    await poll()
  } catch (e) {
    notes.value[c.id] = errorText(e)
  }
}

const describe = (c: TrackerConnection) => [c.capabilities?.displayName, c.baseUrl, c.auth ? AUTH_LABELS[c.auth] : '', c.knowledgeEnabled ? tt('serves Knowledge') : ''].filter(Boolean).join(' · ')
const status = (c: TrackerConnection) => !c.enabled ? tt('Disabled') : c.lastError || (c.healthy ? tt('Working') : tt('Not tested yet'))
const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div>
    <h2 class="set-h">
      {{ tt('Trackers') }}
    </h2>
    <p class="m-0 mb-3 text-[12.5px] text-muted">
      {{ tt('Connect external issue trackers. Board stages pull issues from them and update them, and Knowledge can read their documents.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-2 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
    >
      {{ error }}
    </p>

    <template
      v-for="group in [{ title: tt('On this device'), about: tt('Credentials are in the system keychain and requests go from this device, so instances behind a VPN work. Never synced.'), list: device, scope: TrackerConnectionScope.DEVICE, show: true }, { title: tt('Company'), about: tt('Kept encrypted on the server, which makes the requests. Usable by everyone on their boards; the credentials never reach them.'), list: company, scope: TrackerConnectionScope.ADMIN, show: isAdmin || company.length > 0 }]"
      :key="group.scope"
    >
      <template v-if="group.show">
        <h3 class="mt-4 mb-1 text-[12.5px] text-strong">
          {{ group.title }}
        </h3>
        <p class="m-0 mb-2 text-[12px] text-muted">
          {{ group.about }}
        </p>
        <div
          v-for="c in group.list"
          :key="c.id"
          class="set-row"
        >
          <span class="set-label">
            <span>{{ c.name }}</span>
            <span class="set-desc">{{ describe(c) }}</span>
            <span
              class="set-desc"
              :class="c.lastError ? 'text-bad' : ''"
            >{{ notes[c.id] || status(c) }}</span>
          </span>
          <template v-if="group.scope === TrackerConnectionScope.DEVICE || isAdmin">
            <HBtn
              v-if="c.auth === TrackerAuthMethod.OAUTH"
              size="sm"
              @click="authorize(c)"
            >
              {{ c.hasSecret ? tt('Authorize again') : tt('Authorize') }}
            </HBtn>
            <HBtn
              size="sm"
              @click="test(c)"
            >
              {{ tt('Test') }}
            </HBtn>
            <HBtn
              size="sm"
              @click="edit(c)"
            >
              {{ tt('Change') }}
            </HBtn>
            <HBtn
              size="sm"
              @click="setEnabled(c, !c.enabled)"
            >
              {{ c.enabled ? tt('Disable') : tt('Enable') }}
            </HBtn>
            <HBtn
              size="sm"
              tone="danger"
              :aria-label="tt('Remove {0}', [c.name])"
              @click="remove(c)"
            >
              {{ confirming === c.id ? tt('Remove for good') : tt('Remove') }}
            </HBtn>
          </template>
        </div>
        <HBtn
          v-if="group.scope === TrackerConnectionScope.DEVICE || isAdmin"
          size="sm"
          class="mt-2"
          @click="add(group.scope)"
        >
          {{ tt('Connect a tracker') }}
        </HBtn>
      </template>
    </template>

    <form
      v-if="form.open"
      class="mt-3 flex flex-col gap-2 border border-line p-3"
      @submit.prevent="save"
    >
      <div class="flex flex-wrap items-center gap-2">
        <HSelect
          v-model="form.provider"
          class="w-[130px]"
          :options="providerOptions"
          :aria-label="tt('Provider')"
        />
        <input
          v-model="form.name"
          :placeholder="tt('Name')"
          :aria-label="tt('Name')"
          :class="[inputClass, 'w-[180px]']"
        >
        <input
          v-model="form.baseUrl"
          :placeholder="tt('https://youtrack.example.com')"
          :aria-label="tt('Address')"
          :class="[inputClass, 'min-w-[240px] flex-1']"
        >
      </div>
      <div class="flex flex-wrap items-center gap-2 text-[12.5px] text-toned">
        <span>{{ tt('Sign in with') }}</span>
        <HSelect
          :model-value="String(form.auth)"
          class="w-[170px]"
          :options="authOptions"
          :aria-label="tt('Sign in with')"
          @update:model-value="form.auth = Number($event)"
        />
        <input
          v-if="form.auth === TrackerAuthMethod.PASSWORD"
          v-model="form.login"
          :placeholder="tt('Login')"
          :aria-label="tt('Login')"
          autocomplete="off"
          :class="[inputClass, 'w-[160px]']"
        >
        <input
          v-if="needsSecret"
          v-model="form.secret"
          type="password"
          autocomplete="new-password"
          :placeholder="form.id ? tt('Leave empty to keep the stored one') : form.auth === TrackerAuthMethod.TOKEN ? tt('Permanent token') : tt('Password')"
          :aria-label="form.auth === TrackerAuthMethod.TOKEN ? tt('Token') : tt('Password')"
          :class="[inputClass, 'min-w-[200px] flex-1']"
        >
        <template v-if="form.auth === TrackerAuthMethod.OAUTH">
          <input
            v-model="form.oauthClientId"
            :placeholder="tt('OAuth client id')"
            :aria-label="tt('OAuth client id')"
            :class="[inputClass, 'w-[190px]']"
          >
          <input
            v-model="form.oauthClientSecret"
            type="password"
            autocomplete="new-password"
            :placeholder="tt('Client secret, for a confidential client')"
            :aria-label="tt('Client secret')"
            :class="[inputClass, 'w-[240px]']"
          >
        </template>
      </div>
      <p class="m-0 text-[11.5px] text-muted">
        <template v-if="form.auth === TrackerAuthMethod.TOKEN">
          {{ tt('Make a permanent token in YouTrack under Profile, Account Security. It has your account\'s rights.') }}
        </template>
        <template v-else-if="form.auth === TrackerAuthMethod.OAUTH">
          {{ tt('An admin of the instance creates the OAuth client first, with Authorization Code and PKCE. Save, then Authorize.') }}
        </template>
        <template v-else>
          {{ tt('Uses the same sign-in as the web page. It does not work with two-factor authentication; a token is the sturdier choice.') }}
        </template>
      </p>
      <div class="flex flex-wrap items-center gap-3 text-[12.5px] text-toned">
        <label class="flex items-center gap-1.5">
          <HToggle
            v-model="form.knowledgeEnabled"
            :label="tt('Serve documents to Knowledge')"
          />
          {{ tt('Serve documents to Knowledge') }}
        </label>
        <input
          v-if="form.knowledgeEnabled"
          v-model="form.knowledgeQuery"
          :placeholder="tt('Narrow them: project: HELP')"
          :aria-label="tt('Which documents')"
          :class="[inputClass, 'w-[200px]']"
        >
        <label
          v-if="form.scope === TrackerConnectionScope.ADMIN"
          class="flex items-center gap-1.5"
        >
          <HToggle
            v-model="form.allowPrivateNetwork"
            :label="tt('Allow a private network address')"
          />
          {{ tt('Allow a private network address') }}
        </label>
      </div>
      <div class="flex items-center gap-2">
        <HBtn
          size="sm"
          variant="primary"
          type="submit"
        >
          {{ tt('Save') }}
        </HBtn>
        <HBtn
          size="sm"
          :disabled="form.auth === TrackerAuthMethod.OAUTH"
          @click="testDraft"
        >
          {{ tt('Test') }}
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          @click="form.open = false"
        >
          {{ tt('Cancel') }}
        </HBtn>
        <span class="text-[12px] text-muted">{{ draftNote }}</span>
      </div>
    </form>
  </div>
</template>
