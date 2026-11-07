<script setup lang="ts">
// Settings > Remote: whether this machine takes sessions started elsewhere (device-local, never synced), the chat
// identities linked to this account (only this account sees them), and where sessions a chat app starts run (synced).
import { REMOTE_MODES, type ChatIdentityView, type FolderView, type MachineView } from '~/lib/remote/types'
import type { ChatDefaults } from '~/lib/transport/types'

const store = useSessionStore()
const api = () => store.transport!.remote()

const allow = ref(false)
const policyOff = ref(false)
const chatPolicyOff = ref(false)
const identities = ref<ChatIdentityView[]>([])
const chatAvailable = ref(false)
const link = ref<{ code: string, expiresAt: string } | null>(null)
const machines = ref<MachineView[]>([])
const folders = ref<FolderView[]>([])
const defaults = reactive<ChatDefaults>({ deviceId: '', folder: '', mode: '' })
const error = ref('')
const saved = ref(false)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function refresh() {
  if (!store.transport) return
  error.value = ''
  try {
    allow.value = (await store.transport.getDeviceSettings()).allowRemoteStarts
    const prefs = await store.transport.getUserPreferences().catch(() => undefined)
    policyOff.value = prefs?.managedPolicy?.remoteStartsDisabled ?? false
    chatPolicyOff.value = prefs?.managedPolicy?.chatAppsDisabled ?? false
    if (prefs?.chatDefaults) Object.assign(defaults, prefs.chatDefaults)
    const ids = await api().identities()
    identities.value = ids.identities
    chatAvailable.value = ids.available
    machines.value = await api().machines().catch(() => [])
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(refresh)

async function setAllow(v: boolean) {
  error.value = ''
  try {
    await store.updateDeviceSettings({ allowRemoteStarts: v })
    allow.value = store.deviceSettings.allowRemoteStarts
  } catch (e) {
    error.value = message(e)
    allow.value = !v
  }
}

async function newLink() {
  error.value = ''
  try {
    link.value = await api().startLink()
  } catch (e) {
    error.value = message(e)
  }
}

async function unlink(id: ChatIdentityView) {
  error.value = ''
  try {
    await api().unlink(id)
    await refresh()
  } catch (e) {
    error.value = message(e)
  }
}

const machine = computed(() => machines.value.find(m => m.deviceId === defaults.deviceId))
const machineOptions = computed(() => [{ value: '', label: tt('The most recently seen machine that takes remote starts') }, ...machines.value.filter(m => m.remoteStarts || m.daemon).map(m => ({ value: m.deviceId, label: m.name || tt('Unnamed machine'), hint: m.online ? '' : tt('offline') }))])
const folderOptions = computed(() => {
  const opts = [{ value: '', label: tt('Its most recent folder') }, ...folders.value.map(f => ({ value: f.path, label: f.name || f.path, hint: f.path }))]
  if (defaults.folder && !folders.value.some(f => f.path === defaults.folder)) opts.push({ value: defaults.folder, label: defaults.folder, hint: '' })
  return opts
})
const modeOptions = [{ value: '', label: tt('Default') }, ...REMOTE_MODES.filter(m => m !== 'default').map(m => ({ value: m, label: m }))]

watch(() => machine.value?.hostId, async (hostId) => {
  folders.value = []
  if (!hostId || !machine.value?.online || !machine.value.remoteStarts) return
  folders.value = await api().folders(hostId).catch(() => [])
}, { immediate: true })

async function saveDefaults() {
  error.value = ''
  saved.value = false
  try {
    await store.transport!.updateUserPreferences({ chatDefaults: { ...defaults } })
    saved.value = true
  } catch (e) {
    error.value = message(e)
  }
}
</script>

<template>
  <section
    :aria-label="tt('Remote')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Remote') }}
    </h2>
    <p class="set-sub">
      {{ tt('Start sessions on this machine from your phone or a chat app. Only you can, and only in folders you have worked in here.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div class="set-row">
      <span class="set-label">
        <span>{{ tt('Allow remote starts') }}</span>
        <span class="set-desc">
          {{ policyOff ? tt('Your organization\'s policy turned remote starts off.') : tt('This machine only: the setting stays here and is never synced. Sessions start in one of its recent folders.') }}
        </span>
      </span>
      <HToggle
        :model-value="allow && !policyOff"
        :disabled="policyOff"
        :label="tt('Allow remote starts')"
        @update:model-value="setAllow"
      />
    </div>

    <h3 class="mt-5 mb-1 text-[12.5px] text-strong">
      {{ tt('Chat apps') }}
    </h3>
    <p
      v-if="chatPolicyOff"
      class="m-0 text-[12px] text-muted"
    >
      {{ tt('Your organization\'s policy turned chat apps off.') }}
    </p>
    <p
      v-else-if="!chatAvailable"
      class="m-0 text-[12px] text-muted"
    >
      {{ tt('No chat app is connected to this server. An admin can connect Slack under Chat apps.') }}
    </p>
    <template v-else>
      <p class="m-0 mb-1 text-[12px] text-muted">
        {{ tt('Mention the Slack app or write to it, and a session starts on your machine. Only you see these links.') }}
      </p>
      <div
        v-for="i in identities"
        :key="`${i.teamId}/${i.externalUserId}`"
        class="set-row"
      >
        <span class="set-label">
          <span>{{ i.displayName || i.externalUserId }}</span>
          <span class="set-desc">{{ tt('Slack, linked {0}', [i.linkedVia === 'directory' ? tt('by your directory email') : tt('with a code')]) }}</span>
        </span>
        <HBtn
          size="sm"
          tone="danger"
          @click="unlink(i)"
        >
          {{ tt('Unlink') }}
        </HBtn>
      </div>
      <div class="set-row">
        <span class="set-label">
          <span>{{ tt('Link a Slack account') }}</span>
          <span class="set-desc">
            <template v-if="link">{{ tt('Send this to the Slack app within 15 minutes:') }} <code class="text-strong">link {{ link.code }}</code></template>
            <template v-else>{{ tt('Creates a one-time code you send to the app.') }}</template>
          </span>
        </span>
        <HBtn
          size="sm"
          @click="newLink"
        >
          {{ link ? tt('New code') : tt('Create code') }}
        </HBtn>
      </div>

      <h3 class="mt-5 mb-1 text-[12.5px] text-strong">
        {{ tt('Where chat sessions run') }}
      </h3>
      <form
        class="flex flex-col gap-2"
        @submit.prevent="saveDefaults"
      >
        <label class="flex flex-col gap-1 text-[12px]">
          <span class="text-muted">{{ tt('Machine') }}</span>
          <HSelect
            v-model="defaults.deviceId"
            :options="machineOptions"
            :aria-label="tt('Machine')"
          />
        </label>
        <label class="flex flex-col gap-1 text-[12px]">
          <span class="text-muted">{{ tt('Folder') }}</span>
          <HSelect
            v-model="defaults.folder"
            :options="folderOptions"
            :disabled="!defaults.deviceId"
            :aria-label="tt('Folder')"
          />
        </label>
        <label class="flex flex-col gap-1 text-[12px]">
          <span class="text-muted">{{ tt('Mode') }}</span>
          <HSelect
            v-model="defaults.mode"
            :options="modeOptions"
            :aria-label="tt('Mode')"
          />
        </label>
        <div class="flex items-center justify-end gap-3">
          <span
            v-if="saved"
            role="status"
            class="text-[12px] text-ok"
          >{{ tt('Saved') }}</span>
          <HBtn
            size="md"
            variant="primary"
            type="submit"
          >
            {{ tt('Save') }}
          </HBtn>
        </div>
      </form>
    </template>
  </section>
</template>
