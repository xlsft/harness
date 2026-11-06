<script setup lang="ts">
// Settings > SSO: OIDC and SAML identity providers, next to LDAP. Enabled ones become buttons on the sign-in page, the page the
// desktop app and the CLI open to connect, so no client needs anything else. A Test runs on the form before it is saved, and a
// saved provider offers a real sign-in that shows what came back and creates nothing. The client secret is write-only. Below,
// the requirement to sign in through SSO, and the requirement of a second factor.
import { emptySsoProvider, type MfaPolicy, type SsoProtocol, type SsoProviderView, type SsoSettings, type SsoTestResult } from '~/lib/enterprise/types'
import type { UserRole } from '~/lib/users/types'
import { ROLE_LABEL, lines } from '~/lib/enterprise/types'

const store = useSessionStore()
const access = useAccess()
const writable = computed(() => access.can('sign_in.write'))

const providers = ref<SsoProviderView[]>([])
const settings = ref<SsoSettings>({ requireSso: false, breakGlassLogins: [] })
const breakGlass = ref('')
const mfaPolicy = ref<MfaPolicy>({ required: false, roles: [], exemptSso: false })
const form = ref<SsoProviderView | null>(null)
const scopes = ref('')
const domains = ref('')
const allowedGroups = ref('')
const result = ref<SsoTestResult | null>(null)
const testing = ref(false)
const saving = ref(false)
const error = ref('')
const confirming = ref('')
const loading = ref(true)

function api() {
  return store.transport!.enterprise()
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
    const [p, s, m] = await Promise.all([api().listSso(), api().getSsoSettings(), api().getMfaPolicy()])
    providers.value = p
    settings.value = s
    breakGlass.value = s.breakGlassLogins.join('\n')
    mfaPolicy.value = m
  })
  loading.value = false
}
onMounted(load)

function edit(p: SsoProviderView) {
  form.value = { ...p, clientSecret: '', clearClientSecret: false, roleMappings: [...p.roleMappings] }
  scopes.value = p.scopes.join(' ')
  domains.value = p.allowedDomains.join('\n')
  allowedGroups.value = p.allowedGroups.join('\n')
  result.value = null
}

function add(protocol: SsoProtocol) {
  edit(emptySsoProvider(protocol))
}

function current(): SsoProviderView {
  const f = form.value!
  return { ...f, scopes: scopes.value.split(/\s+/).filter(Boolean), allowedDomains: lines(domains.value), allowedGroups: lines(allowedGroups.value) }
}

async function save() {
  saving.value = true
  const ok = await run(async () => {
    const saved = await api().saveSso(current())
    // Stays open: now it has the URLs to register at the provider, and can be tried.
    edit(saved)
  })
  saving.value = false
  if (ok) await load()
}

async function test() {
  testing.value = true
  result.value = null
  await run(async () => {
    result.value = await api().testSso(current())
  })
  testing.value = false
}

async function remove(p: SsoProviderView) {
  if (confirming.value !== p.id) {
    confirming.value = p.id
    return
  }
  confirming.value = ''
  if (await run(() => api().deleteSso(p.id))) {
    form.value = null
    await load()
  }
}

async function saveSettings() {
  await run(async () => {
    settings.value = await api().setSsoSettings({ requireSso: settings.value.requireSso, breakGlassLogins: lines(breakGlass.value) })
    breakGlass.value = settings.value.breakGlassLogins.join('\n')
  })
  await load()
}

async function saveMfa(patch: Partial<MfaPolicy>) {
  const before = mfaPolicy.value
  mfaPolicy.value = { ...before, ...patch }
  const ok = await run(async () => {
    mfaPolicy.value = await api().setMfaPolicy(mfaPolicy.value)
  })
  if (!ok) mfaPolicy.value = before
}

function toggleMfaRole(r: UserRole) {
  const roles = mfaPolicy.value.roles.includes(r) ? mfaPolicy.value.roles.filter(x => x !== r) : [...mfaPolicy.value.roles, r]
  saveMfa({ roles })
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Select and copy by hand.
  }
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent disabled:opacity-60'
const area = 'min-h-[64px] w-full resize-y border border-accented bg-default px-2 py-1.5 font-mono text-[12px] text-strong outline-none focus:border-accent disabled:opacity-60'
const label = 'flex flex-col gap-1 text-[11.5px] text-muted'
const ROLES: UserRole[] = ['admin', 'auditor', 'billing', 'user']
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ tt('Single sign-on') }}
    </h2>
    <p class="set-sub">
      {{ tt('Sign-in through your identity provider, over OIDC or SAML. Enabled providers show on the sign-in page, which the desktop app and the CLI open to connect, so no client needs setting up. Accounts are created at first sign-in, or provisioned first through Provisioning.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <p
      v-if="loading"
      class="m-0 py-4 text-[12.5px] text-muted"
    >
      {{ tt('Loading…') }}
    </p>
    <template v-else>
      <div
        v-if="providers.length"
        class="mb-2 overflow-hidden border border-default"
      >
        <button
          v-for="p in providers"
          :key="p.id"
          type="button"
          class="flex w-full items-center gap-2 rounded-none border-b border-default px-3 py-2.5 text-left text-[12.5px] last:border-b-0 hover:bg-hover"
          @click="edit(p)"
        >
          <span class="min-w-0 flex-1 truncate text-strong">{{ p.name }}</span>
          <span class="border border-default px-1 text-[10.5px] text-muted">{{ p.protocol.toUpperCase() }}</span>
          <span
            class="text-[11.5px]"
            :class="p.enabled ? 'text-ok' : 'text-muted'"
          >{{ p.enabled ? 'enabled' : 'off' }}</span>
        </button>
      </div>
      <div
        v-if="writable && !form"
        class="mb-4 flex gap-2"
      >
        <HBtn
          size="sm"
          @click="add('oidc')"
        >
          <HIcon
            name="plus"
            :size="12"
          />{{ tt('Add OIDC provider') }}
        </HBtn>
        <HBtn
          size="sm"
          @click="add('saml')"
        >
          <HIcon
            name="plus"
            :size="12"
          />{{ tt('Add SAML provider') }}
        </HBtn>
      </div>

      <form
        v-if="form"
        class="mb-4 flex flex-col gap-3 border border-default p-3"
        :aria-label="`${form.protocol.toUpperCase()} provider`"
        @submit.prevent="save"
      >
        <div class="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
          <label :class="label">{{ tt('Name on the sign-in button') }}
            <input
              v-model="form.name"
              required
              :disabled="!writable"
              :placeholder="tt('Corp SSO')"
              :class="input"
            >
          </label>
          <label class="flex items-center gap-2 self-end text-[12px] text-toned">
            <HToggle
              v-model="form.enabled"
              :disabled="!writable"
              :label="tt('Enabled')"
            />{{ tt('Enabled') }}
          </label>
        </div>

        <template v-if="form.protocol === 'oidc'">
          <label :class="label">{{ tt('Issuer URL') }}
            <input
              v-model="form.issuer"
              :disabled="!writable"
              placeholder="https://idp.example.com/realms/corp"
              spellcheck="false"
              :class="input"
            >
          </label>
          <div class="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
            <label :class="label">{{ tt('Client ID') }}
              <input
                v-model="form.clientId"
                :disabled="!writable"
                spellcheck="false"
                autocomplete="off"
                :class="input"
              >
            </label>
            <label :class="label">{{ tt('Client secret{0}', [form.clientSecretSet ? ' (set; empty keeps it)' : '']) }}
              <input
                v-model="form.clientSecret"
                type="password"
                :disabled="!writable"
                autocomplete="new-password"
                :placeholder="form.clientSecretSet ? '••••••••' : tt('Empty for a public client')"
                :class="input"
              >
            </label>
          </div>
          <label :class="label">{{ tt('Extra scopes, besides openid, email and profile') }}
            <input
              v-model="scopes"
              :disabled="!writable"
              placeholder="groups"
              spellcheck="false"
              :class="input"
            >
          </label>
          <div
            v-if="form.redirectUri"
            class="flex items-center gap-2 text-[11.5px] text-muted"
          >
            <span>{{ tt('Redirect URI to register') }}</span>
            <code class="min-w-0 flex-1 truncate border border-default bg-raised px-2 py-1 text-[11.5px] text-strong select-all">{{ form.redirectUri }}</code>
            <HBtn
              size="xs"
              @click="copy(form.redirectUri)"
            >
              {{ tt('Copy') }}
            </HBtn>
          </div>
        </template>

        <template v-else>
          <label :class="label">{{ tt('Identity provider\'s sign-in URL') }}
            <input
              v-model="form.idpSsoUrl"
              :disabled="!writable"
              placeholder="https://idp.example.com/sso/saml"
              spellcheck="false"
              :class="input"
            >
          </label>
          <label :class="label">{{ tt('Identity provider\'s entity ID') }}
            <input
              v-model="form.idpEntityId"
              :disabled="!writable"
              placeholder="https://idp.example.com/entity"
              spellcheck="false"
              :class="input"
            >
          </label>
          <label :class="label">{{ tt('Identity provider\'s signing certificate (PEM)') }}
            <textarea
              v-model="form.idpCertificate"
              :disabled="!writable"
              placeholder="-----BEGIN CERTIFICATE-----"
              spellcheck="false"
              :class="area"
            />
          </label>
          <dl
            v-if="form.samlAcsUrl"
            class="m-0 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[11.5px]"
          >
            <dt class="text-muted">
              {{ tt('ACS URL') }}
            </dt><dd class="m-0 truncate text-toned select-all">
              {{ form.samlAcsUrl }}
            </dd>
            <dt class="text-muted">
              {{ tt('Entity ID') }}
            </dt><dd class="m-0 truncate text-toned select-all">
              {{ form.samlEntityId }}
            </dd>
            <dt class="text-muted">
              {{ tt('Metadata') }}
            </dt><dd class="m-0 truncate text-toned select-all">
              {{ form.samlMetadataUrl }}
            </dd>
          </dl>
        </template>

        <details class="text-[12px] text-toned">
          <summary class="cursor-pointer text-[12px] text-muted">
            {{ tt('Attribute names, who may sign in') }}
          </summary>
          <div class="mt-3 flex flex-col gap-3">
            <div class="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
              <label :class="label">{{ tt('Email attribute') }}
                <input
                  v-model="form.emailAttribute"
                  :disabled="!writable"
                  placeholder="email"
                  spellcheck="false"
                  :class="input"
                >
              </label>
              <label :class="label">{{ tt('Name attribute') }}
                <input
                  v-model="form.nameAttribute"
                  :disabled="!writable"
                  :placeholder="form.protocol === 'oidc' ? 'name' : 'displayName'"
                  spellcheck="false"
                  :class="input"
                >
              </label>
              <label :class="label">{{ tt('Username attribute') }}
                <input
                  v-model="form.usernameAttribute"
                  :disabled="!writable"
                  :placeholder="form.protocol === 'oidc' ? 'preferred_username' : 'username'"
                  spellcheck="false"
                  :class="input"
                >
              </label>
              <label :class="label">{{ tt('Groups attribute') }}
                <input
                  v-model="form.groupsAttribute"
                  :disabled="!writable"
                  placeholder="groups"
                  spellcheck="false"
                  :class="input"
                >
              </label>
            </div>
            <div class="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
              <label :class="label">{{ tt('Allowed email domains, one per line') }}
                <textarea
                  v-model="domains"
                  :disabled="!writable"
                  placeholder="corp.example.com"
                  spellcheck="false"
                  :class="area"
                />
              </label>
              <label :class="label">{{ tt('Allowed groups, one per line') }}
                <textarea
                  v-model="allowedGroups"
                  :disabled="!writable"
                  spellcheck="false"
                  :class="area"
                />
              </label>
            </div>
            <label class="flex items-center gap-2 text-[12px] text-toned">
              <HToggle
                v-model="form.autoCreateUsers"
                :disabled="!writable"
                :label="tt('Create accounts at first sign-in')"
              />{{ tt('Create accounts at first sign-in') }}
            </label>
          </div>
        </details>

        <div class="flex flex-col gap-1.5">
          <span class="text-[11.5px] text-muted">{{ tt('Group to role rules, re-checked at every sign-in. First match wins; no match is a plain user. No rules: roles are managed in Users.') }}</span>
          <SettingsRoleMappings
            v-model="form.roleMappings"
            :readonly="!writable"
            :placeholder="tt('Group name from the provider')"
          />
        </div>

        <div
          v-if="result"
          role="status"
          class="flex flex-col gap-1 border px-3 py-2 text-[12px]"
          :class="result.ok ? 'border-ok' : 'border-bad'"
        >
          <span
            v-for="c in result.checks"
            :key="c.name"
            :class="c.ok ? 'text-toned' : 'text-bad'"
          >{{ c.ok ? '✓' : '✗' }} {{ c.name }}: {{ c.message }}</span>
          <a
            v-if="result.signInTestUrl"
            :href="result.signInTestUrl"
            target="_blank"
            rel="noopener"
            class="mt-1 text-accent underline"
          >{{ tt('Try a real sign-in (creates nothing, one use)') }}</a>
          <span
            v-else-if="result.ok"
            class="text-muted"
          >{{ tt('Save the provider to try a real sign-in.') }}</span>
        </div>

        <div class="flex flex-wrap items-center justify-end gap-2">
          <HBtn
            v-if="writable && form.id"
            size="md"
            tone="danger"
            @click="remove(form)"
          >
            {{ confirming === form.id ? tt('Confirm delete') : tt('Delete') }}
          </HBtn>
          <span class="flex-1" />
          <HBtn
            size="md"
            variant="ghost"
            tone="muted"
            @click="form = null"
          >
            {{ tt('Close') }}
          </HBtn>
          <template v-if="writable">
            <HBtn
              size="md"
              :disabled="testing"
              @click="test"
            >
              {{ testing ? tt('Testing…') : tt('Test') }}
            </HBtn>
            <HBtn
              size="md"
              variant="primary"
              type="submit"
              :disabled="saving"
            >
              {{ tt('Save') }}
            </HBtn>
          </template>
        </div>
      </form>

      <h3 class="mt-4 mb-1 text-[12.5px] font-semibold text-strong">
        {{ tt('Require SSO') }}
      </h3>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Only through an identity provider') }}</span>
          <span class="set-desc">{{ tt('Local passwords and LDAP are refused, except for the logins below, which keep working when the provider is down.') }}</span>
        </div>
        <HToggle
          v-model="settings.requireSso"
          :disabled="!writable"
          :label="tt('Require SSO')"
        />
      </div>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Break-glass logins') }}</span>
          <span class="set-desc">{{ tt('Usernames or emails, one per line.') }}</span>
        </div>
        <textarea
          v-model="breakGlass"
          :aria-label="tt('Break-glass logins')"
          :disabled="!writable"
          spellcheck="false"
          :class="area"
          class="max-w-[280px]"
        />
      </div>
      <div
        v-if="writable"
        class="mb-4 flex justify-end"
      >
        <HBtn
          size="sm"
          @click="saveSettings"
        >
          {{ tt('Save') }}
        </HBtn>
      </div>

      <h3 class="mt-4 mb-1 text-[12.5px] font-semibold text-strong">
        {{ tt('Two-factor sign-in') }}
      </h3>
      <p class="set-sub">
        {{ tt('People set up an authenticator in Settings, Account. Requiring it asks everyone covered to enroll at their next sign-in, and their connected devices stop working until they did. Turn it on for your own account first.') }}
      </p>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Require a second factor') }}</span>
        </div>
        <HToggle
          :model-value="mfaPolicy.required"
          :disabled="!writable"
          :label="tt('Require a second factor')"
          @update:model-value="saveMfa({ required: $event })"
        />
      </div>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('For these roles') }}</span>
          <span class="set-desc">{{ tt('None ticked: everyone.') }}</span>
        </div>
        <div class="flex gap-3 text-[12px] text-toned">
          <label
            v-for="r in ROLES"
            :key="r"
            class="flex items-center gap-1.5"
          >
            <input
              type="checkbox"
              :checked="mfaPolicy.roles.includes(r)"
              :disabled="!writable"
              @change="toggleMfaRole(r)"
            >{{ ROLE_LABEL[r] }}
          </label>
        </div>
      </div>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Trust the identity provider') }}</span>
          <span class="set-desc">{{ tt('People signing in through SSO are not asked again.') }}</span>
        </div>
        <HToggle
          :model-value="mfaPolicy.exemptSso"
          :disabled="!writable"
          :label="tt('Do not ask people signing in through SSO')"
          @update:model-value="saveMfa({ exemptSso: $event })"
        />
      </div>
    </template>
  </div>
</template>
