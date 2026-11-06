<script setup lang="ts">
// Settings > Account: who you are (avatar, name, handle, role, how you sign in), your password (local accounts),
// the devices signed in to your account, and signing out of this computer (desktop).
import { AVATAR_MAX_BYTES, AVATAR_MAX_SIDE, AVATAR_TYPES, avatarUrl, type AccountDevice } from '~/lib/account/types'
import { isDesktop, logout as desktopLogout } from '~/lib/desktop'

const ROLE_NAME: Record<string, string> = { admin: 'Admin', auditor: 'Auditor', billing: 'Billing' }

const store = useSessionStore()
const toast = useToast()
const desktop = isDesktop()
const { profile, error, load, update } = useAccountProfile()

const devices = ref<AccountDevice[]>([])
const devicesError = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function loadDevices() {
  if (!store.transport) return
  try {
    devices.value = await store.transport.account().devices()
    devicesError.value = ''
  } catch (e) {
    devicesError.value = message(e)
  }
}
onMounted(() => {
  load(true)
  loadDevices()
})

const host = computed(() => {
  try {
    return store.account.server ? new URL(store.account.server).host : ''
  } catch {
    return store.account.server
  }
})
const initial = computed(() => (profile.value?.name || profile.value?.username || store.userEmail || '?').charAt(0).toUpperCase())
const ldap = computed(() => profile.value?.source === 'ldap')
// Who manages the name and password when it is not this server.
const signInLabel = computed(() => ({ ldap: 'LDAP', sso: 'SSO', scim: 'SSO' } as Record<string, string>)[profile.value?.source ?? ''] ?? 'Password')
const managedBy = computed(() => ({ ldap: 'The directory (LDAP)', sso: 'Your identity provider' } as Record<string, string>)[profile.value?.source ?? ''] ?? '')

function date(iso: string) {
  return iso ? new Date(iso).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) : '—'
}
function ago(iso: string) {
  if (!iso) return 'never'
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 90) return 'just now'
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  if (s < 86400 * 30) return `${Math.round(s / 86400)} d ago`
  return date(iso)
}

// Avatar
const picker = ref<HTMLInputElement | null>(null)
const busy = ref(false)
async function onPick(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  ;(e.target as HTMLInputElement).value = ''
  if (!file) return
  busy.value = true
  try {
    await update({ image: await avatarDataUrl(file, AVATAR_MAX_SIDE, AVATAR_MAX_BYTES) })
  } catch (err) {
    toast.add({ title: 'Could not set the picture', description: message(err), color: 'error' })
  } finally {
    busy.value = false
  }
}
async function removeAvatar() {
  busy.value = true
  try {
    await update({ image: '' })
  } catch (err) {
    toast.add({ title: 'Could not remove the picture', description: message(err), color: 'error' })
  } finally {
    busy.value = false
  }
}

// Name
const editing = ref(false)
const name = ref('')
function editName() {
  name.value = profile.value?.name ?? ''
  editing.value = true
}
async function saveName() {
  const v = name.value.trim()
  if (!v || v === profile.value?.name) {
    editing.value = false
    return
  }
  try {
    await update({ name: v })
    editing.value = false
  } catch (err) {
    toast.add({ title: 'Could not rename', description: message(err), color: 'error' })
  }
}

// Password
const pw = reactive({ current: '', next: '', confirm: '', others: false })
const pwError = ref('')
const pwBusy = ref(false)
const pwReady = computed(() => pw.current && pw.next.length >= 8 && pw.next === pw.confirm)
async function changePassword() {
  if (!store.transport || !pwReady.value) return
  pwBusy.value = true
  try {
    await store.transport.account().changePassword(pw.current, pw.next, pw.others)
    Object.assign(pw, { current: '', next: '', confirm: '', others: false })
    pwError.value = ''
    toast.add({ title: 'Password changed', color: 'neutral' })
    loadDevices()
  } catch (err) {
    pwError.value = message(err)
  } finally {
    pwBusy.value = false
  }
}
const mismatch = computed(() => pw.confirm !== '' && pw.next !== pw.confirm)

// Devices: revoking asks once more on the same button.
const confirmRevoke = ref('')
async function revoke(d: AccountDevice) {
  if (!store.transport) return
  if (confirmRevoke.value !== d.id) {
    confirmRevoke.value = d.id
    return
  }
  confirmRevoke.value = ''
  try {
    await store.transport.account().revokeDevice(d.id)
    devices.value = devices.value.filter(x => x.id !== d.id)
  } catch (err) {
    toast.add({ title: 'Could not sign the device out', description: message(err), color: 'error' })
  }
}

async function signOut() {
  await desktopLogout()
  window.location.reload()
}

// The red outline of every sign-out button on the page.
const danger = 'h-8 shrink-0 border border-bad px-3 text-[12.5px] text-bad hover:bg-bad hover:text-on-accent'
const input = 'h-8 w-full border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <h2 class="set-h">
    Account
  </h2>
  <p class="set-sub">
    Who you are on {{ host || 'this server' }}.
  </p>
  <p
    v-if="error"
    class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
  >
    {{ error }}
  </p>

  <div class="set-row !items-center !justify-start gap-4">
    <button
      type="button"
      class="group relative size-16 shrink-0 overflow-hidden rounded-box border border-line outline-none focus-visible:border-accent"
      :aria-label="profile?.image ? 'Change picture' : 'Add a picture'"
      :disabled="busy"
      @click="picker?.click()"
    >
      <img
        v-if="profile?.image"
        :src="avatarUrl(profile)"
        alt=""
        class="size-full object-cover"
      >
      <span
        v-else
        class="flex size-full items-center justify-center bg-accent text-[24px] font-bold text-on-accent"
      >{{ initial }}</span>
      <span class="absolute inset-0 hidden items-center justify-center bg-ground/60 text-strong group-hover:flex">
        <HIcon
          :name="busy ? 'loading' : 'upload'"
          :spin="busy"
        />
      </span>
    </button>
    <input
      ref="picker"
      type="file"
      :accept="AVATAR_TYPES.join(',')"
      class="hidden"
      @change="onPick"
    >
    <div class="flex min-w-0 flex-1 flex-col gap-1">
      <div
        v-if="editing"
        class="flex items-center gap-2"
      >
        <input
          v-model="name"
          aria-label="Display name"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          :class="input"
          class="max-w-[280px]"
          maxlength="80"
          @keydown.enter="saveName"
          @keydown.esc.stop="editing = false"
        >
        <HBtn
          variant="primary"
          size="md"
          @click="saveName"
        >
          Save
        </HBtn>
        <HBtn
          size="md"
          tone="muted"
          @click="editing = false"
        >
          Cancel
        </HBtn>
      </div>
      <div
        v-else
        class="flex items-center gap-2"
      >
        <span class="truncate text-[15px] font-semibold text-strong">{{ profile?.name || profile?.username || store.userEmail }}</span>
        <button
          v-if="profile && !ldap"
          type="button"
          aria-label="Edit name"
          class="text-muted hover:text-strong"
          @click="editName"
        >
          <HIcon
            name="edit"
            :size="13"
          />
        </button>
      </div>
      <span class="truncate text-[12px] text-muted">
        <template v-if="profile?.username">@{{ profile.username }} · </template>{{ profile?.email || store.userEmail }}
      </span>
      <span class="flex gap-1.5 text-[11px]">
        <span
          v-if="profile"
          class="border px-1.5"
          :class="profile.role === 'admin' ? 'border-accent text-accent' : 'border-line text-muted'"
        >{{ ROLE_NAME[profile.role] ?? 'Member' }}</span>
        <span
          v-if="ldap"
          class="border border-line px-1.5 text-muted"
        >Directory account</span>
      </span>
    </div>
    <HBtn
      v-if="profile?.image"
      size="md"
      tone="muted"
      :disabled="busy"
      @click="removeAvatar"
    >
      Remove picture
    </HBtn>
  </div>
  <div class="set-row">
    <div class="set-label">
      <span>Signs in with</span>
      <span class="set-desc">{{ managedBy ? `${managedBy} manages your name and password.` : profile?.source === 'scim' ? 'Provisioned by your organization. You sign in through SSO.' : 'A password stored on this server.' }}</span>
    </div>
    <span class="text-[12.5px] text-strong">{{ signInLabel }}</span>
  </div>
  <div class="set-row">
    <div class="set-label">
      <span>Member since</span>
    </div>
    <span class="text-[12.5px] text-strong">{{ date(profile?.createdAt ?? '') }}</span>
  </div>
  <div class="set-row">
    <div class="set-label">
      <span>Last sign-in</span>
    </div>
    <span class="text-[12.5px] text-strong">{{ ago(profile?.lastSignInAt ?? '') }}</span>
  </div>

  <SettingsMfa v-if="profile" />

  <template v-if="profile && !ldap && profile.source !== 'sso' && profile.source !== 'scim'">
    <h2 class="set-h mt-8">
      Password
    </h2>
    <p class="set-sub">
      At least 8 characters.
    </p>
    <form
      class="set-row flex-col !items-stretch gap-2.5"
      @submit.prevent="changePassword"
    >
      <div class="grid grid-cols-3 gap-2 max-md:grid-cols-1">
        <input
          v-model="pw.current"
          type="password"
          autocomplete="current-password"
          placeholder="Current password"
          aria-label="Current password"
          :class="input"
        >
        <input
          v-model="pw.next"
          type="password"
          autocomplete="new-password"
          placeholder="New password"
          aria-label="New password"
          :class="input"
        >
        <input
          v-model="pw.confirm"
          type="password"
          autocomplete="new-password"
          placeholder="Repeat new password"
          aria-label="Repeat new password"
          :class="[input, mismatch ? '!border-bad' : '']"
        >
      </div>
      <div class="flex items-center gap-3">
        <label class="flex flex-1 items-center gap-2 text-[12px] text-muted">
          <input
            v-model="pw.others"
            type="checkbox"
            class="accent-(--color-accent)"
          >Sign out everywhere else
        </label>
        <span
          v-if="pwError || mismatch"
          class="text-[12px] text-bad"
        >{{ mismatch ? 'The passwords differ.' : pwError }}</span>
        <HBtn
          type="submit"
          variant="primary"
          size="md"
          :disabled="!pwReady || pwBusy"
        >
          Change password
        </HBtn>
      </div>
    </form>
  </template>

  <h2 class="set-h mt-8">
    Devices
  </h2>
  <p class="set-sub">
    Computers signed in to your account. Signing one out stops its agent from reaching the server.
  </p>
  <p
    v-if="devicesError"
    class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
  >
    {{ devicesError }}
  </p>
  <div
    v-for="d in devices"
    :key="d.id"
    class="set-row !items-center"
  >
    <HIcon
      name="monitor"
      class="mt-px shrink-0 text-muted"
    />
    <div class="set-label min-w-0 flex-1">
      <span class="flex items-center gap-2">
        <span class="truncate">{{ d.name || 'Unnamed device' }}</span>
        <span
          v-if="d.current"
          class="border border-accent px-1.5 text-[11px] text-accent"
        >this device</span>
      </span>
      <span class="set-desc">{{ [d.os && `${d.os}/${d.arch}`, d.clientName].filter(Boolean).join(' · ') || 'Unknown client' }} · signed in {{ date(d.createdAt) }} · seen {{ ago(d.lastSeenAt) }}</span>
    </div>
    <button
      v-if="!d.current"
      type="button"
      :class="danger"
      @click="revoke(d)"
      @blur="confirmRevoke = ''"
    >
      {{ confirmRevoke === d.id ? 'Sign out?' : 'Sign out' }}
    </button>
  </div>

  <div
    v-if="desktop"
    class="set-row mt-8"
  >
    <div class="set-label">
      <span>Sign out of this computer</span>
      <span class="set-desc">Stops the agent here and forgets its credentials.</span>
    </div>
    <button
      type="button"
      :class="danger"
      @click="signOut"
    >
      Sign out
    </button>
  </div>
</template>
