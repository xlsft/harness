<script setup lang="ts">
// Settings > Users (admins): the server's accounts. Search, add a user (password, generated once, or a
// directory user), and per user: role, reset password, disable / enable, delete, their devices and web
// sessions, and their personal providers (key set or not; keys are never shown). The server enforces the
// rules (admin only, the last admin stays, nobody disables or deletes themselves); errors show inline.
import type { NewUser, UserProviderView, UserRole, UserSessions, UsersApi, UserView } from '~/lib/users/types'
import { ROLE_HINT, ROLE_LABEL, downloadCsv } from '~/lib/enterprise/types'

// Upstream protocol of a user's provider, as shown in the list.
const PROTOCOL_LABEL: Record<string, string> = {
  'anthropic': 'Anthropic Messages',
  'openai-chat': 'OpenAI Chat Completions',
  'openai-responses': 'OpenAI Responses',
  'decision-model': 'Decision model',
  'http': 'HTTP'
}

const ROLES: UserRole[] = ['user', 'admin', 'auditor', 'billing']
const roleOptions = ROLES.map(r => ({ value: r, label: ROLE_LABEL[r] }))
const SOURCE_LABEL: Record<UserView['source'], string> = { local: 'Local password', ldap: 'LDAP directory', sso: 'Identity provider', scim: 'Provisioned, signs in through SSO' }

const store = useSessionStore()
const access = useAccess()
// Auditors and billing see this list too; what they may not change, the screen does not offer.
const writable = computed(() => access.can('users.write'))
const canSeeSessions = computed(() => access.can('users.write') || access.can('sign_in.read'))
const canReadQuotas = computed(() => access.can('quotas.read'))
const canWriteQuotas = computed(() => access.can('quotas.write'))

const users = ref<UserView[]>([])
const query = ref('')
const loading = ref(true)
const error = ref('')
// A password shown once after it was generated: for whom and the value.
const revealed = ref<{ email: string, password: string } | null>(null)

function api(): UsersApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.users()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function run(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
    return true
  } catch (e) {
    error.value = message(e)
    return false
  }
}

async function load() {
  await run(async () => {
    users.value = await api().list(query.value)
  })
  loading.value = false
}
onMounted(async () => {
  await access.load()
  await load()
})

let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(query, () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(load, 200)
})

function replace(u: UserView) {
  users.value = users.value.map(x => x.id === u.id ? u : x)
}

function ago(iso: string) {
  return iso ? formatAge(Date.now() - new Date(iso).getTime()) : 'never'
}

function day(iso: string) {
  return iso ? new Date(iso).toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' }) : '—'
}

// --- Add -----------------------------------------------------------------------------------------------

const emptyForm = (): NewUser => ({ name: '', username: '', email: '', password: '', generatePassword: true, role: 'user', ldap: false })
const form = ref<NewUser | null>(null)
const saving = ref(false)

async function create() {
  const f = form.value
  if (!f) return
  saving.value = true
  const ok = await run(async () => {
    const r = await api().create(f)
    if (r.generatedPassword) revealed.value = { email: r.user.email, password: r.generatedPassword }
  })
  saving.value = false
  if (ok) {
    form.value = null
    await load()
  }
}

// --- One user ------------------------------------------------------------------------------------------

const open = ref('')
const sessions = ref<UserSessions | null>(null)
const providers = ref<UserProviderView[]>([])
const newPassword = ref('')
const confirming = ref('')

async function toggle(u: UserView) {
  confirming.value = ''
  newPassword.value = ''
  if (open.value === u.id) {
    open.value = ''
    return
  }
  open.value = u.id
  sessions.value = null
  providers.value = []
  if (!canSeeSessions.value) return
  await run(async () => {
    const [s, p] = await Promise.all([api().sessions(u.id), api().providers(u.id)])
    if (open.value !== u.id) return
    sessions.value = s
    providers.value = p
  })
}

// Destructive actions ask once more: the first click arms, the second runs.
function armed(key: string) {
  if (confirming.value === key) {
    confirming.value = ''
    return true
  }
  confirming.value = key
  return false
}

async function setRole(u: UserView, role: UserRole) {
  await run(async () => replace(await api().setRole(u.id, role)))
}

async function setDisabled(u: UserView) {
  if (!u.disabled && !armed(`disable:${u.id}`)) return
  await run(async () => {
    replace(await api().setDisabled(u.id, !u.disabled))
    if (open.value === u.id) sessions.value = await api().sessions(u.id)
  })
}

async function resetPassword(u: UserView, generate: boolean) {
  await run(async () => {
    const p = await api().resetPassword(u.id, generate ? { generate: true } : { password: newPassword.value })
    newPassword.value = ''
    revealed.value = generate ? { email: u.email, password: p } : null
  })
}

async function remove(u: UserView) {
  if (!armed(`delete:${u.id}`)) return
  if (await run(() => api().remove(u.id))) {
    open.value = ''
    await load()
  }
}

async function revoke(u: UserView, deviceId?: string) {
  if (!deviceId && !armed(`revoke:${u.id}`)) return
  await run(async () => {
    await api().revokeSessions(u.id, deviceId)
    sessions.value = await api().sessions(u.id)
  })
}

async function removeProvider(u: UserView, p: UserProviderView) {
  if (!armed(`provider:${p.id}`)) return
  await run(async () => {
    await api().removeProvider(u.id, p.id)
    providers.value = await api().providers(u.id)
  })
}

async function resetMfa(u: UserView) {
  if (!armed(`mfa:${u.id}`)) return
  await run(async () => {
    await store.transport!.enterprise().resetUserMfa(u.id)
    await load()
  })
}

async function exportUsage(u: UserView) {
  await run(async () => {
    const r = await store.transport!.enterprise().exportUsage({ userId: u.id })
    downloadCsv(`harness-usage-${u.username || u.id}.csv`, r.csv)
  })
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Select and copy by hand.
  }
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ tt('Users') }}
    </h2>
    <p class="set-sub">
      {{ tt('Everyone with an account on this server. {0}', [writable ? '' : tt('Your role can read this; only admins change it.')]) }}
    </p>

    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="revealed"
      role="status"
      class="mb-3 flex flex-col gap-2 border border-accent px-3 py-2.5 text-[12px]"
    >
      <span class="text-strong">{{ tt('New password for {0}', [revealed.email]) }}</span>
      <span class="text-muted">{{ tt('Shown only now. Hand it over securely; it cannot be shown again.') }}</span>
      <div class="flex items-center gap-2">
        <code class="flex-1 truncate border border-default bg-raised px-2 py-1 font-mono text-[12.5px] text-strong select-all">{{ revealed.password }}</code>
        <HBtn
          size="sm"
          @click="copy(revealed.password)"
        >
          {{ tt('Copy') }}
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          tone="muted"
          @click="revealed = null"
        >
          {{ tt('Done') }}
        </HBtn>
      </div>
    </div>

    <div class="mb-2 flex items-center gap-2">
      <input
        v-model="query"
        type="search"
        :aria-label="tt('Search users')"
        :placeholder="tt('Search by name, username or email')"
        :class="input"
        class="max-w-[320px]"
      >
      <span class="flex-1" />
      <HBtn
        v-if="!form && writable"
        size="sm"
        @click="form = emptyForm()"
      >
        <HIcon
          name="plus"
          :size="12"
        />{{ tt('Add user') }}
      </HBtn>
    </div>

    <form
      v-if="form"
      class="mb-2 flex flex-col gap-3 border border-default p-3"
      :aria-label="tt('New user')"
      @submit.prevent="create"
    >
      <div class="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
        <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Name') }}
          <input
            v-model="form.name"
            required
            autocomplete="off"
            :class="input"
          >
        </label>
        <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Username{0}', [form.ldap ? ' (directory)' : ' (optional)']) }}
          <input
            v-model="form.username"
            :required="form.ldap"
            autocomplete="off"
            autocapitalize="none"
            spellcheck="false"
            pattern="[a-zA-Z0-9_.]{3,30}"
            title="3-30 letters, digits, _ or ."
            :class="input"
          >
        </label>
        <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Email') }}
          <input
            v-model="form.email"
            type="email"
            required
            autocomplete="off"
            :class="input"
          >
        </label>
        <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Role') }}
          <HSelect
            v-model="form.role"
            :aria-label="tt('Role')"
            :options="roleOptions"
          />
        </label>
      </div>
      <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-[12px] text-toned">
        <label class="flex items-center gap-2">
          <HToggle
            v-model="form.ldap"
            :label="tt('Directory user')"
          />{{ tt('Signs in through LDAP (no local password)') }}
        </label>
        <label
          v-if="!form.ldap"
          class="flex items-center gap-2"
        >
          <HToggle
            v-model="form.generatePassword"
            :label="tt('Generate a password')"
          />{{ tt('Generate a password and show it once') }}
        </label>
      </div>
      <label
        v-if="!form.ldap && !form.generatePassword"
        class="flex flex-col gap-1 text-[11.5px] text-muted"
      >{{ tt('Password') }}
        <input
          v-model="form.password"
          type="password"
          minlength="8"
          required
          autocomplete="new-password"
          :class="input"
          class="max-w-[320px]"
        >
      </label>
      <div class="flex justify-end gap-2">
        <HBtn
          size="md"
          variant="ghost"
          tone="muted"
          @click="form = null"
        >
          {{ tt('Cancel') }}
        </HBtn>
        <HBtn
          size="md"
          variant="primary"
          type="submit"
          :disabled="saving"
        >
          {{ tt('Add') }}
        </HBtn>
      </div>
    </form>

    <p
      v-if="loading"
      class="m-0 py-4 text-[12.5px] text-muted"
    >
      {{ tt('Loading…') }}
    </p>
    <div
      v-else-if="users.length"
      class="overflow-hidden border border-default"
    >
      <div
        v-for="u in users"
        :key="u.id"
        class="flex flex-col border-b border-default last:border-b-0"
      >
        <button
          type="button"
          class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-none px-3 py-2.5 text-left text-[12.5px] hover:bg-hover"
          :aria-expanded="open === u.id"
          @click="toggle(u)"
        >
          <span class="flex min-w-[220px] flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 truncate text-strong">
              {{ u.name }}
              <span
                v-if="u.isSelf"
                class="text-[11px] text-muted"
              >{{ tt('you') }}</span>
              <span
                v-if="u.role !== 'user'"
                class="border border-default px-1 text-[10.5px] text-muted"
              >{{ u.role }}</span>
              <span
                v-if="u.source !== 'local'"
                class="border border-default px-1 text-[10.5px] text-muted"
              >{{ u.source === 'ldap' ? 'LDAP' : u.source === 'sso' ? 'SSO' : 'SCIM' }}</span>
              <span
                v-if="u.mfaEnabled"
                class="border border-default px-1 text-[10.5px] text-muted"
              >{{ tt('2FA') }}</span>
              <span
                v-if="u.disabled"
                class="border border-bad px-1 text-[10.5px] text-bad"
              >{{ tt('disabled') }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">
              {{ [u.username ? `@${u.username}` : '', u.email].filter(Boolean).join(' · ') }}
            </span>
          </span>
          <span class="text-[11.5px] text-dimmed">{{ tt('signed in {0}', [ago(u.lastSignInAt)]) }}</span>
          <HIcon
            :name="open === u.id ? 'up' : 'down'"
            :size="12"
            class="text-muted"
          />
        </button>

        <div
          v-if="open === u.id"
          class="flex flex-col gap-3 border-t border-default px-3 py-3 text-[12px]"
        >
          <dl class="m-0 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[11.5px]">
            <dt class="text-muted">
              {{ tt('Created') }}
            </dt><dd class="m-0 text-toned">
              {{ day(u.createdAt) }}
            </dd>
            <dt class="text-muted">
              {{ tt('Last sign-in') }}
            </dt><dd class="m-0 text-toned">
              {{ u.lastSignInAt ? day(u.lastSignInAt) : 'never' }}
            </dd>
            <dt class="text-muted">
              {{ tt('Last seen') }}
            </dt><dd class="m-0 text-toned">
              {{ u.lastSeenAt ? ago(u.lastSeenAt) : 'never' }}
            </dd>
            <dt class="text-muted">
              {{ tt('Sign-in') }}
            </dt><dd class="m-0 text-toned">
              {{ SOURCE_LABEL[u.source] }}
            </dd>
            <template v-if="u.groups.length">
              <dt class="text-muted">
                {{ tt('Groups') }}
              </dt><dd class="m-0 text-toned">
                {{ u.groups.join(', ') }}
              </dd>
            </template>
            <template v-if="u.disabled">
              <dt class="text-muted">
                {{ tt('Disabled') }}
              </dt><dd class="m-0 text-bad">
                {{ u.disabledReason || 'yes' }}
              </dd>
            </template>
          </dl>

          <div
            v-if="writable"
            class="flex flex-wrap items-center gap-1.5"
          >
            <div
              role="radiogroup"
              :aria-label="tt('Role of {0}', [u.name])"
              class="mr-2 flex gap-[2px]"
            >
              <button
                v-for="r in ROLES"
                :key="r"
                type="button"
                role="radio"
                :aria-checked="u.role === r"
                :title="ROLE_HINT[r]"
                class="border px-2 py-[3px] text-[11.5px] capitalize"
                :class="u.role === r ? 'border-line bg-raised text-strong' : 'border-transparent text-muted hover:text-strong'"
                @click="u.role !== r && setRole(u, r)"
              >
                {{ r }}
              </button>
            </div>
            <HBtn
              v-if="!u.isSelf"
              size="sm"
              :tone="u.disabled ? 'default' : 'danger'"
              @click="setDisabled(u)"
            >
              {{ u.disabled ? tt('Enable') : confirming === `disable:${u.id}` ? tt('Confirm disable') : tt('Disable') }}
            </HBtn>
            <HBtn
              v-if="!u.isSelf"
              size="sm"
              tone="danger"
              @click="remove(u)"
            >
              {{ confirming === `delete:${u.id}` ? tt('Confirm delete {0}', [u.email]) : tt('Delete') }}
            </HBtn>
            <HBtn
              v-if="u.mfaEnabled"
              size="sm"
              @click="resetMfa(u)"
            >
              {{ confirming === `mfa:${u.id}` ? tt('Confirm: remove their authenticator') : tt('Reset 2FA') }}
            </HBtn>
          </div>

          <SettingsUserQuota
            v-if="canReadQuotas"
            :user-id="u.id"
            :name="u.name"
            :readonly="!canWriteQuotas"
            @error="error = $event"
          />
          <div v-if="access.can('usage.read')">
            <HBtn
              size="xs"
              @click="exportUsage(u)"
            >
              <HIcon
                name="download"
                :size="11"
              />{{ tt('Usage as CSV') }}
            </HBtn>
          </div>

          <div
            v-if="u.source === 'local' && writable"
            class="flex flex-col gap-1.5"
          >
            <span class="text-[11.5px] text-muted">{{ tt('Password') }}</span>
            <form
              class="flex flex-wrap items-center gap-2"
              @submit.prevent="resetPassword(u, false)"
            >
              <input
                v-model="newPassword"
                type="password"
                minlength="8"
                autocomplete="new-password"
                :aria-label="tt('New password for {0}', [u.name])"
                :placeholder="tt('New password')"
                :class="input"
                class="h-7 max-w-[220px]"
              >
              <HBtn
                size="sm"
                type="submit"
                :disabled="newPassword.length < 8"
              >
                {{ tt('Set') }}
              </HBtn>
              <HBtn
                size="sm"
                @click="resetPassword(u, true)"
              >
                {{ tt('Generate new') }}
              </HBtn>
            </form>
          </div>
          <p
            v-else-if="u.source === 'ldap' || u.source === 'sso'"
            class="m-0 text-[11.5px] text-muted"
          >
            {{ tt('The password is managed in {0}.', [u.source === 'ldap' ? 'the directory' : 'the identity provider']) }}
          </p>

          <div
            v-if="canSeeSessions"
            class="flex flex-col gap-1.5"
          >
            <div class="flex items-center gap-2 text-[11.5px] text-muted">
              <span>{{ tt('Devices and sessions') }}</span>
              <span class="flex-1" />
              <HBtn
                v-if="writable && sessions && (sessions.devices.length || sessions.webSessions)"
                size="xs"
                tone="danger"
                @click="revoke(u)"
              >
                {{ confirming === `revoke:${u.id}` ? tt('Confirm sign out everywhere') : tt('Sign out everywhere') }}
              </HBtn>
            </div>
            <span
              v-if="!sessions"
              class="text-[11.5px] text-dimmed"
            >{{ tt('Loading…') }}</span>
            <template v-else>
              <div
                v-for="d in sessions.devices"
                :key="d.id"
                class="flex items-center gap-2 text-[11.5px]"
              >
                <span class="truncate text-toned">{{ d.name }}</span>
                <span class="truncate text-dimmed">{{ tt('{0} · seen {1}', [[d.os, d.arch].filter(Boolean).join('/'), ago(d.lastSeenAt)]) }}</span>
                <span class="flex-1" />
                <HIconBtn
                  v-if="writable"
                  icon="x"
                  :size="26"
                  :icon-size="12"
                  :label="tt('Revoke {0}', [d.name])"
                  @click="revoke(u, d.id)"
                />
              </div>
              <span class="text-[11.5px] text-dimmed">
                {{ tt('{0}{1} web {2}.', [sessions.devices.length ? '' : tt('No connected devices. '), sessions.webSessions, sessions.webSessions === 1 ? 'session' : 'sessions']) }}
              </span>
            </template>
          </div>

          <div
            v-if="canSeeSessions"
            class="flex flex-col gap-1.5"
          >
            <span class="text-[11.5px] text-muted">{{ tt('Personal providers') }}</span>
            <div
              v-for="p in providers"
              :key="p.id"
              class="flex items-center gap-2 text-[11.5px]"
            >
              <span class="truncate text-toned">{{ p.name }}</span>
              <span class="truncate text-dimmed">
                {{ [PROTOCOL_LABEL[p.type] ?? p.type, p.keySet ? 'key set' : 'no key', `${p.modelCount} ${p.modelCount === 1 ? 'model' : 'models'}`, p.enabled ? '' : 'off'].filter(Boolean).join(' · ') }}
              </span>
              <span class="flex-1" />
              <HBtn
                v-if="writable"
                size="xs"
                tone="danger"
                @click="removeProvider(u, p)"
              >
                {{ confirming === `provider:${p.id}` ? tt('Confirm remove') : tt('Remove') }}
              </HBtn>
            </div>
            <span
              v-if="sessions && !providers.length"
              class="text-[11.5px] text-dimmed"
            >{{ tt('None.') }}</span>
          </div>
        </div>
      </div>
    </div>
    <p
      v-else
      class="m-0 border border-default px-3 py-5 text-center text-[12.5px] text-muted"
    >
      {{ query ? tt('No users match.') : tt('No users.') }}
    </p>

    <SettingsGroups
      v-if="canReadQuotas || access.can('directory.read')"
      :users="users"
      @changed="load"
    />
  </div>
</template>
