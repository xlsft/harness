<script setup lang="ts">
// Settings > Users > LDAP (admins): sign-in against an LDAP / Active Directory directory. The sign-in page then
// checks the directory (and local accounts per the chosen order); a successful bind creates or updates the
// local account. The search account's password is write-only. "Test connection" and "Test user lookup" run
// against the form as it is, before saving.
import { ROLE_LABEL } from '~/lib/enterprise/types'
import type { LdapSettings, LdapUserLookup, UsersApi } from '~/lib/users/types'
import { DEFAULT_LDAP_FILTER } from '~/lib/users/types'

const access = useAccess()
// Auditors read this; the fieldset below locks every control for them.
const writable = computed(() => access.can('sign_in.write'))
const store = useSessionStore()

const s = ref<LdapSettings | null>(null)
const urls = ref('')
const breakGlass = ref('')
const error = ref('')
const saved = ref(false)
const saving = ref(false)
const testing = ref(false)
const test = ref<{ ok: boolean, message: string } | null>(null)
const login = ref('')
const lookup = ref<LdapUserLookup | null>(null)

function api(): UsersApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.users()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function show(v: LdapSettings) {
  s.value = v
  urls.value = v.urls.join('\n')
  breakGlass.value = v.breakGlassLogins.join('\n')
}

onMounted(async () => {
  try {
    show(await api().getLdap())
  } catch (e) {
    error.value = message(e)
  }
})

const lines = (v: string) => v.split(/[\n,]/).map(x => x.trim()).filter(Boolean)

// The form as settings (lists from their text areas).
function current(): LdapSettings {
  return { ...s.value!, urls: lines(urls.value), breakGlassLogins: lines(breakGlass.value) }
}

async function save() {
  if (!s.value) return
  saving.value = true
  saved.value = false
  try {
    show(await api().setLdap(current()))
    error.value = ''
    saved.value = true
  } catch (e) {
    error.value = message(e)
  } finally {
    saving.value = false
  }
}

async function testConnection() {
  testing.value = true
  try {
    test.value = await api().testLdap(current())
  } catch (e) {
    test.value = { ok: false, message: message(e) }
  } finally {
    testing.value = false
  }
}

async function testUser() {
  if (!login.value.trim()) return
  testing.value = true
  try {
    lookup.value = await api().testLdapUser(current(), login.value.trim())
  } catch (e) {
    lookup.value = { found: false, message: message(e), dn: '', username: '', email: '', name: '', groups: [], allowed: false, admin: false, role: 'user', userId: '' }
  } finally {
    testing.value = false
  }
}

const input = 'h-8 w-[300px] border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent max-sm:w-full'
const area = 'min-h-16 w-[300px] border border-accented bg-default px-2 py-1.5 font-mono text-[12px] text-strong outline-none focus:border-accent max-sm:w-full'
</script>

<template>
  <section
    aria-label="LDAP"
    class="flex flex-col"
  >
    <h2 class="set-h">
      LDAP
    </h2>
    <p class="set-sub">
      Sign in with directory accounts (OpenLDAP, Active Directory). The sign-in page checks the directory; a successful sign-in creates or updates the account here, without a local password.
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <form
      v-if="s"
      class="flex flex-col"
      @submit.prevent="save"
    >
      <fieldset
        :disabled="!writable"
        class="m-0 flex flex-col border-0 p-0"
      >
        <div class="set-row">
          <div class="set-label">
            <span>LDAP sign-in</span>
            <span class="set-desc">Off: only local accounts sign in.</span>
          </div>
          <HToggle
            v-model="s.enabled"
            label="Enable LDAP sign-in"
          />
        </div>

        <div class="set-row">
          <div class="set-label">
            <span>Servers</span>
            <span class="set-desc">ldap:// or ldaps:// URLs, one per line, tried in order.</span>
          </div>
          <textarea
            v-model="urls"
            aria-label="LDAP server URLs"
            placeholder="ldaps://ldap.example.com"
            spellcheck="false"
            :class="area"
          />
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>StartTLS</span>
            <span class="set-desc">Upgrade ldap:// connections to TLS.</span>
          </div>
          <HToggle
            v-model="s.startTls"
            label="Use StartTLS"
          />
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>CA certificate</span>
            <span class="set-desc">PEM, for a private CA. Empty: the system roots.</span>
          </div>
          <textarea
            v-model="s.caCert"
            aria-label="CA certificate"
            placeholder="-----BEGIN CERTIFICATE-----"
            spellcheck="false"
            :class="area"
          />
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Skip certificate check</span>
            <span class="set-desc">Accepts any server certificate.</span>
          </div>
          <HToggle
            v-model="s.skipVerify"
            label="Skip certificate verification"
          />
        </div>
        <p
          v-if="s.skipVerify"
          role="alert"
          class="m-0 mb-2 border border-bad px-3 py-2 text-[12px] text-bad"
        >
          Insecure: anyone between this server and the directory can read passwords. Use it only to test, then add the CA certificate instead.
        </p>

        <div class="set-row">
          <div class="set-label">
            <span>Search account</span>
            <span class="set-desc">Bind DN to search users with. Empty: anonymous search.</span>
          </div>
          <div class="flex flex-col gap-2">
            <input
              v-model="s.bindDn"
              aria-label="Bind DN"
              placeholder="cn=harness,ou=services,dc=example,dc=com"
              spellcheck="false"
              :class="input"
            >
            <input
              v-model="s.bindPassword"
              type="password"
              aria-label="Bind password"
              autocomplete="new-password"
              :placeholder="s.bindPasswordSet ? 'Password set; type to replace' : 'Bind password'"
              :class="input"
            >
            <label
              v-if="s.bindPasswordSet"
              class="flex items-center gap-2 text-[11.5px] text-muted"
            >
              <input
                v-model="s.clearBindPassword"
                type="checkbox"
              >Remove the stored password
            </label>
          </div>
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>User search</span>
            <span class="set-desc">Base DN and filter; {{ '{' + '{username}' + '}' }} is the login, escaped.</span>
          </div>
          <div class="flex flex-col gap-2">
            <input
              v-model="s.userBaseDn"
              aria-label="User search base DN"
              placeholder="ou=people,dc=example,dc=com"
              spellcheck="false"
              :class="input"
            >
            <input
              v-model="s.userFilter"
              aria-label="User filter"
              :placeholder="DEFAULT_LDAP_FILTER"
              spellcheck="false"
              :class="input"
              class="font-mono"
            >
          </div>
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Attributes</span>
            <span class="set-desc">Username, email and display name (AD: sAMAccountName, mail, displayName).</span>
          </div>
          <div class="flex w-[300px] gap-2 max-sm:w-full">
            <input
              v-model="s.usernameAttribute"
              aria-label="Username attribute"
              placeholder="uid"
              spellcheck="false"
              :class="input"
              class="w-0 min-w-0 flex-1"
            >
            <input
              v-model="s.emailAttribute"
              aria-label="Email attribute"
              placeholder="mail"
              spellcheck="false"
              :class="input"
              class="w-0 min-w-0 flex-1"
            >
            <input
              v-model="s.nameAttribute"
              aria-label="Display name attribute"
              placeholder="cn"
              spellcheck="false"
              :class="input"
              class="w-0 min-w-0 flex-1"
            >
          </div>
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Groups</span>
            <span class="set-desc">Allowed: only its members sign in. Admins: its members become admins, everyone else users.</span>
          </div>
          <div class="flex flex-col gap-2">
            <input
              v-model="s.allowedGroupDn"
              aria-label="Allowed group DN"
              placeholder="Allowed group DN (optional)"
              spellcheck="false"
              :class="input"
            >
            <input
              v-model="s.adminGroupDn"
              aria-label="Admin group DN"
              placeholder="Admin group DN (optional)"
              spellcheck="false"
              :class="input"
            >
          </div>
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Group to role rules</span>
            <span class="set-desc">Group DN to role, re-checked at every sign-in; the admin group goes first. First match wins, no match is a plain user. Empty: roles are managed in Users.</span>
          </div>
          <SettingsRoleMappings
            v-model="s.roleMappings"
            placeholder="cn=auditors,ou=groups,dc=example,dc=com"
            class="max-w-[420px]"
          />
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Create accounts</span>
            <span class="set-desc">On first sign-in. Off: add directory users under Users first.</span>
          </div>
          <HToggle
            v-model="s.autoCreateUsers"
            label="Create accounts on first sign-in"
          />
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Local accounts</span>
            <span class="set-desc">Who may still sign in with a local password.</span>
          </div>
          <div class="flex flex-col gap-2">
            <HSelect
              v-model="s.localLogin"
              aria-label="Local accounts"
              :options="[{ value: 'fallback', label: 'Everyone, when the directory refuses' }, { value: 'break_glass', label: 'Only the break-glass admins below' }]"
            />
            <textarea
              v-if="s.localLogin === 'break_glass'"
              v-model="breakGlass"
              aria-label="Break-glass logins"
              placeholder="Usernames or emails, one per line"
              spellcheck="false"
              :class="area"
            />
          </div>
        </div>

        <div class="set-row">
          <div class="set-label">
            <span>Test</span>
            <span class="set-desc">Uses the form as it is, before saving.</span>
          </div>
          <div class="flex w-[300px] flex-col gap-2 max-sm:w-full">
            <HBtn
              size="sm"
              :disabled="testing"
              class="self-start"
              @click="testConnection"
            >
              Test connection
            </HBtn>
            <p
              v-if="test"
              role="status"
              class="m-0 text-[11.5px]"
              :class="test.ok ? 'text-toned' : 'text-bad'"
            >
              {{ test.message }}
            </p>
            <div class="flex gap-2">
              <input
                v-model="login"
                aria-label="Login to look up"
                placeholder="Username or email"
                autocapitalize="none"
                spellcheck="false"
                :class="input"
                class="h-7 w-0 min-w-0 flex-1"
                @keydown.enter.prevent="testUser"
              >
              <HBtn
                size="sm"
                :disabled="testing || !login.trim()"
                @click="testUser"
              >
                Test user lookup
              </HBtn>
            </div>
            <div
              v-if="lookup"
              role="status"
              class="flex flex-col gap-0.5 text-[11.5px]"
            >
              <span :class="lookup.found && lookup.allowed ? 'text-toned' : 'text-bad'">{{ lookup.message }}</span>
              <template v-if="lookup.found">
                <span class="break-all text-muted">{{ lookup.dn }}</span>
                <span class="text-muted">{{ [lookup.username, lookup.email, lookup.name].filter(Boolean).join(' · ') }}</span>
                <span class="text-muted">{{ ROLE_LABEL[lookup.role] }} · {{ lookup.userId ? 'has an account here' : 'no account here yet' }}</span>
                <span
                  v-if="lookup.groups.length"
                  class="break-all text-dimmed"
                >{{ lookup.groups.join('; ') }}</span>
              </template>
            </div>
          </div>
        </div>

        <div class="flex items-center justify-end gap-3 pt-3">
          <span
            v-if="saved"
            role="status"
            class="text-[12px] text-muted"
          >Saved</span>
          <HBtn
            size="md"
            variant="primary"
            type="submit"
            :disabled="saving"
          >
            Save
          </HBtn>
        </div>
      </fieldset>
    </form>
  </section>
</template>
