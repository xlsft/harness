<script setup lang="ts">
// Settings > Knowledge sources: wikis and knowledge bases Knowledge reads live, separate from issue trackers. Laid out like Issue
// trackers: personal connections on this device (keychain, requests from here, never synced), company ones on the server for admins.
// A source may use the sign-in of a tracker connection of the same scope whose provider it can borrow from, so nobody signs in twice.
// Everything comes from the providers' capabilities; adding a provider touches no Vue file (docs/design/tracker.md).
import { openExternal } from '~/lib/desktop'
import { PROVIDER_LOGOS } from '~/utils/providerLogos'
import { errorText } from '~/lib/library/types'
import {
  AUTH_LABELS,
  TrackerAuthMethod,
  TrackerConnectionScope,
  providerTile,
  type KnowledgeCapabilities,
  type KnowledgeConnection,
  type SignInForm,
  type SignInSpec,
  type TrackerCapabilities,
  type TrackerConnection
} from '~/lib/issues/types'

const store = useSessionStore()
const api = () => store.transport!.issues()
const connections = ref<KnowledgeConnection[]>([])
const deviceProviders = ref<KnowledgeCapabilities[]>([])
const companyProviders = ref<KnowledgeCapabilities[]>([])
const trackers = ref<TrackerConnection[]>([])
const trackerProviders = ref<TrackerCapabilities[]>([])
const isAdmin = ref(false)
const error = ref('')
const notes = ref<Record<string, string>>({})
const confirming = ref('')

// '' in borrow: its own sign-in.
const blankSignIn = (): SignInForm => ({ id: '', baseUrl: '', auth: TrackerAuthMethod.TOKEN, login: '', secret: '', oauthClientId: '', oauthClientSecret: '', options: {} })
const form = reactive({ open: false, scope: TrackerConnectionScope.DEVICE, provider: '', name: '', enabled: true, allowPrivateNetwork: false, query: '', borrow: '', signIn: blankSignIn() })

async function refresh() {
  if (!store.transport) return
  try {
    const r = await api().listKnowledgeConnections()
    connections.value = r.connections
    deviceProviders.value = r.deviceProviders
    companyProviders.value = r.companyProviders
    const t = await api().listConnections().catch(() => ({ connections: [], providers: [] }))
    trackers.value = t.connections
    trackerProviders.value = t.providers
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
const providersOf = (scope: TrackerConnectionScope) => (scope === TrackerConnectionScope.DEVICE ? deviceProviders.value : companyProviders.value)
const capabilityOf = (scope: TrackerConnectionScope, provider: string) => providersOf(scope).find(p => p.provider === provider)
const capability = computed(() => capabilityOf(form.scope, form.provider))
const spec = computed((): SignInSpec => {
  const c = capability.value
  return { authMethods: c?.authMethods ?? [], optionFields: c?.optionFields ?? [], authHints: c?.authHints ?? [], baseUrlPlaceholder: c?.baseUrlPlaceholder ?? '', defaultBaseUrl: c?.defaultBaseUrl ?? '' }
})
// Tracker connections of the same scope whose sign-in a provider can use.
const lenders = (scope: TrackerConnectionScope, provider: string) => {
  const from = capabilityOf(scope, provider)?.borrowFrom ?? []
  return trackers.value.filter(t => t.scope === scope && from.includes(t.provider))
}
const signInOptions = computed(() => [
  ...lenders(form.scope, form.provider).map(t => ({ value: t.id, label: tt('Use the sign-in of {0}', [t.name]) })),
  ...(spec.value.authMethods.length ? [{ value: '', label: tt('Its own sign-in') }] : [])
])
const trackerName = (provider: string) => trackerProviders.value.find(p => p.provider === provider)?.displayName ?? provider
const monogram = (scope: TrackerConnectionScope, provider: string) => {
  const c = capabilityOf(scope, provider)
  return providerTile(c?.logo ?? '', c?.displayName ?? provider, PROVIDER_LOGOS)
}

function add(scope: TrackerConnectionScope, provider: string) {
  const c = capabilityOf(scope, provider)
  const lender = lenders(scope, provider)[0]
  Object.assign(form, { open: true, scope, provider, name: '', enabled: true, allowPrivateNetwork: false, query: '', borrow: lender?.id ?? '', signIn: { ...blankSignIn(), auth: c?.authMethods[0] ?? TrackerAuthMethod.TOKEN } })
  draftNote.value = ''
}

function kinds(scope: TrackerConnectionScope, list: KnowledgeConnection[]): { provider: string, displayName: string, capabilities?: KnowledgeCapabilities }[] {
  const out: { provider: string, displayName: string, capabilities?: KnowledgeCapabilities }[] = providersOf(scope).map(p => ({ provider: p.provider, displayName: p.displayName, capabilities: p }))
  for (const c of list) {
    if (!out.some(k => k.provider === c.provider)) out.push({ provider: c.provider, displayName: c.provider })
  }
  return out
}
// What a source gives and how it signs in: on its own, or with a tracker connection's sign-in.
function about(k: { capabilities?: KnowledgeCapabilities }) {
  const p = k.capabilities
  if (!p) return tt('No longer offered here')
  const ways = p.authMethods.map(m => AUTH_LABELS[m]).filter(Boolean).join(', ')
  const borrow = p.borrowFrom.map(trackerName).join(', ')
  const signIn = ways && borrow ? tt('sign-in: {0}, or that of a {1} connection', [ways, borrow]) : borrow ? tt('sign-in of a {0} connection', [borrow]) : tt('sign-in: {0}', [ways])
  return [p.description, signIn].filter(Boolean).join(' · ')
}

function edit(c: KnowledgeConnection) {
  Object.assign(form, {
    open: true, scope: c.scope, provider: c.provider, name: c.name, enabled: c.enabled, allowPrivateNetwork: c.allowPrivateNetwork, query: c.query, borrow: c.borrow?.id ?? '',
    signIn: { ...blankSignIn(), id: c.id, baseUrl: c.baseUrl, auth: c.auth || (capabilityOf(c.scope, c.provider)?.authMethods[0] ?? TrackerAuthMethod.TOKEN), login: c.login, oauthClientId: c.oauthClientId, options: { ...c.options } }
  })
  draftNote.value = ''
}

function input() {
  const s = form.signIn
  const own = !form.borrow
  return {
    id: s.id,
    provider: form.provider,
    name: form.name.trim(),
    baseUrl: own ? s.baseUrl.trim() : '',
    auth: own ? s.auth : TrackerAuthMethod.UNSPECIFIED,
    login: own ? s.login.trim() : '',
    oauthClientId: own ? s.oauthClientId.trim() : '',
    secret: own && s.secret ? s.secret : undefined,
    oauthClientSecret: own && s.oauthClientSecret ? s.oauthClientSecret : undefined,
    enabled: form.enabled,
    allowPrivateNetwork: form.allowPrivateNetwork,
    query: form.query.trim(),
    options: { ...s.options },
    borrowConnectionId: form.borrow
  }
}

async function save() {
  try {
    await api().saveKnowledgeConnection(form.scope, input())
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
    const r = form.signIn.id && !form.signIn.secret ? await api().testKnowledgeConnection(form.scope, { id: form.signIn.id }) : await api().testKnowledgeConnection(form.scope, { draft: input() })
    draftNote.value = r.ok ? tt('Signed in as {0}', [r.displayName || r.login]) : r.error
  } catch (e) {
    draftNote.value = errorText(e)
  }
}

async function test(c: KnowledgeConnection) {
  notes.value[c.id] = tt('Testing…')
  try {
    const r = await api().testKnowledgeConnection(c.scope, { id: c.id })
    notes.value[c.id] = r.ok ? tt('Signed in as {0}', [r.displayName || r.login]) : r.error
    await refresh()
  } catch (e) {
    notes.value[c.id] = errorText(e)
  }
}

async function setEnabled(c: KnowledgeConnection, enabled: boolean) {
  try {
    await api().saveKnowledgeConnection(c.scope, {
      id: c.id, provider: c.provider, name: c.name, baseUrl: c.baseUrl, auth: c.auth, login: c.login, oauthClientId: c.oauthClientId, enabled, allowPrivateNetwork: c.allowPrivateNetwork,
      query: c.query, options: { ...c.options }, borrowConnectionId: c.borrow?.id ?? ''
    })
    await refresh()
  } catch (e) {
    error.value = errorText(e)
  }
}

async function remove(c: KnowledgeConnection) {
  if (confirming.value !== c.id) {
    confirming.value = c.id
    return
  }
  try {
    await api().deleteKnowledgeConnection(c.scope, c.id)
    confirming.value = ''
    await refresh()
  } catch (e) {
    error.value = errorText(e)
  }
}

let timer: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(timer))

async function authorize(c: KnowledgeConnection) {
  try {
    const { url, flowId } = await api().startKnowledgeOAuth(c.scope, c.id)
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

const describe = (c: KnowledgeConnection) =>
  [c.capabilities?.displayName, c.borrow?.id ? tt('signs in as {0}', [c.borrowName || c.borrow.id]) : [c.baseUrl, c.auth ? AUTH_LABELS[c.auth] : ''].filter(Boolean).join(' · '), c.query].filter(Boolean).join(' · ')
const status = (c: KnowledgeConnection) => !c.enabled ? tt('Disabled') : c.lastError || (c.healthy ? tt('Working') : tt('Not tested yet'))
const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div>
    <h2 class="set-h">
      {{ tt('Knowledge sources') }}
    </h2>
    <p class="set-sub">
      {{ tt('Connect wikis and knowledge bases. Knowledge and the agents read their documents live; nothing of them is stored. A source can use the sign-in of an issue tracker connection of the same vendor.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <template
      v-for="group in [{ title: tt('On this device'), about: tt('Only you read these. Credentials are in the system keychain and requests go from this device. Never synced.'), list: device, scope: TrackerConnectionScope.DEVICE, show: true }, { title: tt('Company'), about: tt('Kept on the server, which makes the requests. Everyone can search them; the credentials never reach them.'), list: company, scope: TrackerConnectionScope.ADMIN, show: isAdmin || company.length > 0 }]"
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
            v-for="(k, ki) in kinds(group.scope, group.list)"
            :key="k.provider"
            class="flex flex-col"
            :class="{ 'border-t border-default': ki > 0 }"
          >
            <div class="flex items-center gap-3 px-3 py-2.5">
              <SettingsMonogram
                :text="monogram(group.scope, k.provider)"
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
                :text="monogram(group.scope, c.provider)"
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
                  v-if="!c.borrow?.id && c.auth === TrackerAuthMethod.OAUTH"
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
              :aria-label="form.signIn.id ? tt('Change {0}', [form.name]) : tt('Connect a knowledge source')"
              class="flex flex-col gap-2 border-t border-default px-3 py-3 pl-[52px]"
              @submit.prevent="save"
            >
              <input
                v-model="form.name"
                :placeholder="tt('Name')"
                :aria-label="tt('Name')"
                :class="[inputClass, 'w-[220px]']"
              >
              <div
                v-if="signInOptions.length > 1 || form.borrow"
                class="flex flex-wrap items-center gap-2 text-[12.5px] text-toned"
              >
                <span>{{ tt('Sign-in') }}</span>
                <div class="w-[300px] shrink-0">
                  <HSelect
                    v-model="form.borrow"
                    :options="signInOptions"
                    :aria-label="tt('Sign-in')"
                  />
                </div>
              </div>
              <p
                v-if="form.borrow"
                class="m-0 text-[11.5px] text-muted"
              >
                {{ tt('Signs in as that tracker connection, every time: a new token there is used here at once, and nothing is copied.') }}
              </p>
              <SettingsProviderSignIn
                v-if="!form.borrow"
                v-model="form.signIn"
                :spec="spec"
              />
              <SettingsProviderSignIn
                v-else-if="spec.optionFields.length"
                v-model="form.signIn"
                :spec="{ ...spec, authMethods: [] }"
                :address="false"
              />
              <input
                v-if="capability?.queryLanguage"
                v-model="form.query"
                :placeholder="tt('Narrow them, in {0}: {1}', [capability.queryLanguage, capability.queryExample])"
                :aria-label="tt('Which documents')"
                :class="[inputClass, 'w-[320px]']"
              >
              <label
                v-if="form.scope === TrackerConnectionScope.ADMIN && !form.borrow"
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
                  :disabled="!form.borrow && form.signIn.auth === TrackerAuthMethod.OAUTH"
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
