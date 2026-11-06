<script setup lang="ts">
// Settings > Provisioning: SCIM 2.0, for an identity provider to create, update and remove users and groups. The base URL and
// tokens to give it (a token shows once), and which groups give which role.
import type { RoleMapping, ScimState } from '~/lib/enterprise/types'

const store = useSessionStore()
const access = useAccess()
const writable = computed(() => access.can('sign_in.write'))

const state = ref<ScimState | null>(null)
const mappings = ref<RoleMapping[]>([])
const name = ref('')
const revealed = ref<{ name: string, value: string } | null>(null)
const error = ref('')
const confirming = ref('')

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
    state.value = await api().getScim()
    mappings.value = [...state.value.roleMappings]
  })
}
onMounted(load)

async function create() {
  if (await run(async () => {
    const r = await api().createScimToken(name.value)
    revealed.value = { name: r.token.name, value: r.value }
    name.value = ''
  })) await load()
}

async function revoke(id: string) {
  if (confirming.value !== id) {
    confirming.value = id
    return
  }
  confirming.value = ''
  if (await run(() => api().revokeScimToken(id))) await load()
}

async function saveMappings() {
  await run(async () => {
    mappings.value = await api().setScimMappings(mappings.value)
  })
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Select and copy by hand.
  }
}

const when = (iso: string) => iso ? new Date(iso).toLocaleString() : 'never'
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ tt('Provisioning') }}
    </h2>
    <p class="set-sub">
      {{ tt('SCIM 2.0: your identity provider creates and updates users and groups here, and deactivating someone there signs them out here. Provisioned accounts have no password and sign in through SSO.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <template v-if="state">
      <div class="mb-4 flex items-center gap-2 text-[12px] text-muted">
        <span>{{ tt('SCIM base URL') }}</span>
        <code class="min-w-0 flex-1 truncate border border-default bg-raised px-2 py-1 text-[12px] text-strong select-all">{{ state.baseUrl }}</code>
        <HBtn
          size="xs"
          @click="copy(state.baseUrl)"
        >
          {{ tt('Copy') }}
        </HBtn>
      </div>

      <div
        v-if="revealed"
        role="status"
        class="mb-3 flex flex-col gap-2 border border-accent px-3 py-2.5 text-[12px]"
      >
        <span class="text-strong">{{ tt('Token {0}', [revealed.name]) }}</span>
        <span class="text-muted">{{ tt('Shown only now. Paste it into your identity provider; it cannot be shown again.') }}</span>
        <div class="flex items-center gap-2">
          <code class="flex-1 truncate border border-default bg-raised px-2 py-1 font-mono text-[12.5px] text-strong select-all">{{ revealed.value }}</code>
          <HBtn
            size="sm"
            @click="copy(revealed.value)"
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

      <h3 class="m-0 mb-2 text-[12.5px] font-semibold text-strong">
        {{ tt('Tokens') }}
      </h3>
      <div
        v-if="state.tokens.length"
        class="mb-2 overflow-hidden border border-default"
      >
        <div
          v-for="t in state.tokens"
          :key="t.id"
          class="flex items-center gap-2 border-b border-default px-3 py-2 text-[12.5px] last:border-b-0"
        >
          <span class="min-w-0 flex-1 truncate text-strong">{{ t.name }}</span>
          <span class="text-[11.5px] text-dimmed">{{ tt('last used {0}', [when(t.lastUsedAt)]) }}</span>
          <HBtn
            v-if="writable"
            size="xs"
            tone="danger"
            @click="revoke(t.id)"
          >
            {{ confirming === t.id ? tt('Confirm revoke') : tt('Revoke') }}
          </HBtn>
        </div>
      </div>
      <p
        v-else
        class="m-0 mb-2 text-[12px] text-muted"
      >
        {{ tt('No tokens yet.') }}
      </p>
      <form
        v-if="writable"
        class="mb-4 flex items-center gap-2"
        @submit.prevent="create"
      >
        <input
          v-model="name"
          :placeholder="tt('Name, e.g. Okta')"
          :aria-label="tt('Token name')"
          class="h-8 max-w-[260px] flex-1 border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent"
        >
        <HBtn
          size="sm"
          type="submit"
          :disabled="!name.trim()"
        >
          {{ tt('Create token') }}
        </HBtn>
      </form>

      <h3 class="m-0 mb-1 text-[12.5px] font-semibold text-strong">
        {{ tt('Group to role rules') }}
      </h3>
      <p class="set-sub">
        {{ tt('Applied when a provisioned group\'s members change. First match wins; no match is a plain user. No rules: roles are managed in Users.') }}
      </p>
      <SettingsRoleMappings
        v-model="mappings"
        :readonly="!writable"
        :placeholder="tt('SCIM group display name')"
      />
      <div
        v-if="writable"
        class="mt-2 flex justify-end"
      >
        <HBtn
          size="sm"
          @click="saveMappings"
        >
          {{ tt('Save rules') }}
        </HBtn>
      </div>
    </template>
    <p
      v-else-if="!error"
      class="m-0 py-4 text-[12.5px] text-muted"
    >
      {{ tt('Loading…') }}
    </p>
  </div>
</template>
