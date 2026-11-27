<script setup lang="ts">
// Settings > Issue trackers: external trackers as providers. Personal connections stay on this device, with their credentials in the
// system keychain; company ones are the server's, for admins to manage. Secrets are write-only and never come back here. Everything a
// provider needs in the form comes from its capabilities, so adding a provider touches no Vue file (docs/design/tracker.md).
import { openExternal } from '~/lib/desktop'
import { PROVIDER_LOGOS } from '~/utils/providerLogos'
import { errorText } from '~/lib/library/types'
import { AUTH_LABELS, TrackerAuthMethod, TrackerConnectionScope, providerTile, type SignInForm, type SignInSpec, type TrackerCapabilities, type TrackerConnection } from '~/lib/issues/types'

const store = useSessionStore()
const api = () => store.transport!.issues()
const connections = ref<TrackerConnection[]>([])
const providers = ref<TrackerCapabilities[]>([])
const isAdmin = ref(false)
const error = ref('')
const notes = ref<Record<string, string>>({})
const confirming = ref('')

const blankSignIn = (): SignInForm => ({ id: '', baseUrl: '', auth: TrackerAuthMethod.TOKEN, login: '', secret: '', oauthClientId: '', oauthClientSecret: '', options: {} })
const form = reactive({ open: false, scope: TrackerConnectionScope.DEVICE, provider: '', name: '', enabled: true, allowPrivateNetwork: false, signIn: blankSignIn() })

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
const spec = computed((): SignInSpec => {
  const c = capability.value
  return { authMethods: c?.authMethods ?? [], optionFields: c?.optionFields ?? [], authHints: c?.authHints ?? [], baseUrlPlaceholder: c?.baseUrlPlaceholder ?? '', defaultBaseUrl: c?.defaultBaseUrl ?? '' }
})
const capabilityOf = (provider: string) => providers.value.find(p => p.provider === provider)
const monogram = (provider: string) => {
  const c = capabilityOf(provider)
  return providerTile(c?.logo ?? '', c?.displayName ?? provider, PROVIDER_LOGOS)
}

function add(scope: TrackerConnectionScope, provider: string) {
  const first = capabilityOf(provider)?.authMethods[0] ?? TrackerAuthMethod.TOKEN
  Object.assign(form, { open: true, scope, provider, name: '', enabled: true, allowPrivateNetwork: false, signIn: { ...blankSignIn(), auth: first } })
  draftNote.value = ''
}

// One row per tracker the server supports, plus any a connection uses that it no longer lists; the connections of each
// sit under its row, like the providers in Settings > Models.
function kinds(list: TrackerConnection[]): { provider: string, displayName: string, capabilities?: TrackerCapabilities }[] {
  const out: { provider: string, displayName: string, capabilities?: TrackerCapabilities }[] = providers.value.map(p => ({ provider: p.provider, displayName: p.displayName, capabilities: p }))
  for (const c of list) {
    if (!out.some(k => k.provider === c.provider)) out.push({ provider: c.provider, displayName: c.provider })
  }
  return out
}
// What a tracker gives, and how it signs in, from what the server says it supports.
function about(k: { capabilities?: TrackerCapabilities }) {
  const p = k.capabilities
  if (!p) return tt('No longer offered by this server')
  const ways = p.authMethods.map(m => AUTH_LABELS[m]).filter(Boolean).join(', ')
  return tt('Issues for boards; sign-in: {0}.', [ways])
}

function edit(c: TrackerConnection) {
  Object.assign(form, {
    open: true, scope: c.scope, provider: c.provider, name: c.name, enabled: c.enabled, allowPrivateNetwork: c.allowPrivateNetwork,
    signIn: { ...blankSignIn(), id: c.id, baseUrl: c.baseUrl, auth: c.auth, login: c.login, oauthClientId: c.oauthClientId, options: { ...c.options } }
  })
  draftNote.value = ''
}

function input() {
  const s = form.signIn
  return {
    id: s.id,
    provider: form.provider,
    name: form.name.trim(),
    baseUrl: s.baseUrl.trim(),
    auth: s.auth,
    login: s.login.trim(),
    oauthClientId: s.oauthClientId.trim(),
    secret: s.secret || undefined,
    oauthClientSecret: s.oauthClientSecret || undefined,
    enabled: form.enabled,
    allowPrivateNetwork: form.allowPrivateNetwork,
    options: { ...s.options }
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
    const r = form.signIn.id && !form.signIn.secret ? await api().testConnection(form.scope, { id: form.signIn.id }) : await api().testConnection(form.scope, { draft: input() })
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
    await api().saveConnection(c.scope, { id: c.id, provider: c.provider, name: c.name, baseUrl: c.baseUrl, auth: c.auth, login: c.login, oauthClientId: c.oauthClientId, enabled, allowPrivateNetwork: c.allowPrivateNetwork, options: { ...c.options } })
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

const describe = (c: TrackerConnection) => [c.capabilities?.displayName, c.baseUrl, c.auth ? AUTH_LABELS[c.auth] : ''].filter(Boolean).join(' · ')
const status = (c: TrackerConnection) => !c.enabled ? tt('Disabled') : c.lastError || (c.healthy ? tt('Working') : tt('Not tested yet'))
const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div>
    <h2 class="set-h">
      {{ tt('Issue trackers') }}
    </h2>
    <p class="set-sub">
      {{ tt('Connect external issue trackers. Board stages pull issues from them and update them. Their documents are knowledge sources of their own, in Knowledge sources.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <template
      v-for="group in [{ title: tt('On this device'), about: tt('Credentials are in the system keychain and requests go from this device, so instances behind a VPN work. Never synced.'), list: device, scope: TrackerConnectionScope.DEVICE, show: true }, { title: tt('Company'), about: tt('Kept encrypted on the server, which makes the requests. Usable by everyone on their boards; the credentials never reach them.'), list: company, scope: TrackerConnectionScope.ADMIN, show: isAdmin || company.length > 0 }]"
      :key="group.scope"
    >
      <section
        v-if="group.show"
        :aria-label="group.title"
        class="mb-8 flex flex-col"
      >
        <h3 class="m-0 text-[13px] font-semibold text-strong">
          {{ group.title }}
        </h3>
        <p class="m-0 mt-1 mb-3 text-[12px] text-muted">
          {{ group.about }}
        </p>
        <div class="overflow-hidden border border-default">
          <div
            v-for="(k, ki) in kinds(group.list)"
            :key="k.provider"
            class="flex flex-col"
            :class="{ 'border-t border-default': ki > 0 }"
          >
            <div class="flex items-center gap-3 px-3 py-2.5">
              <SettingsMonogram
                :text="monogram(k.provider)"
                :dim="!group.list.some(c => c.provider === k.provider)"
              />
              <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                <span class="flex items-center gap-2 text-[12.5px] text-strong">
                  {{ k.displayName }}
                  <span
                    v-if="group.list.some(c => c.provider === k.provider)"
                    class="text-[11px] text-ok"
                  >{{ tt('connected') }}</span>
                </span>
                <span class="truncate text-[11.5px] text-muted">{{ about(k) }}</span>
              </span>
              <SettingsLink
                v-if="k.capabilities?.docsUrl"
                :href="k.capabilities.docsUrl"
                class="max-sm:hidden"
              >
                {{ tt('Docs') }}
              </SettingsLink>
              <HBtn
                v-if="k.capabilities && (group.scope === TrackerConnectionScope.DEVICE || isAdmin) && !(form.open && form.scope === group.scope && form.provider === k.provider && !form.signIn.id)"
                size="sm"
                @click="add(group.scope, k.provider)"
              >
                {{ tt('Connect') }}
              </HBtn>
              <span
                v-else-if="group.scope !== TrackerConnectionScope.DEVICE && !isAdmin"
                class="text-[11px] text-dimmed"
              >{{ tt('admins connect it') }}</span>
            </div>
            <div
              v-for="c in group.list.filter(x => x.provider === k.provider)"
              :key="c.id"
              class="flex items-center gap-3 border-t border-default py-2.5 pr-3 pl-[52px]"
            >
              <SettingsMonogram
                :text="monogram(c.provider)"
                :dim="!c.enabled"
              />
              <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                <span class="flex items-center gap-2 text-[12.5px] text-strong">
                  {{ c.name }}
                  <span
                    v-if="!c.enabled"
                    class="text-[11px] text-dimmed"
                  >{{ tt('disabled') }}</span>
                </span>
                <span class="truncate text-[11.5px] text-muted">{{ describe(c) }}</span>
                <span
                  class="truncate text-[11.5px]"
                  :class="c.lastError ? 'text-bad' : 'text-muted'"
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
            <form
              v-if="form.open && form.scope === group.scope && form.provider === k.provider"
              :aria-label="form.signIn.id ? tt('Change {0}', [form.name]) : tt('Connect a tracker')"
              class="flex flex-col gap-2 border-t border-default px-3 py-3 pl-[52px]"
              @submit.prevent="save"
            >
              <input
                v-model="form.name"
                :placeholder="tt('Name')"
                :aria-label="tt('Name')"
                :class="[inputClass, 'w-[220px]']"
              >
              <SettingsProviderSignIn
                v-model="form.signIn"
                :spec="spec"
              />
              <label
                v-if="form.scope === TrackerConnectionScope.ADMIN"
                class="flex items-center gap-1.5 text-[12.5px] text-toned"
              >
                <HToggle
                  v-model="form.allowPrivateNetwork"
                  :label="tt('Allow a private network address')"
                />
                {{ tt('Allow a private network address') }}
              </label>
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
                  :disabled="form.signIn.auth === TrackerAuthMethod.OAUTH"
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
        </div>
      </section>
    </template>
  </div>
</template>
