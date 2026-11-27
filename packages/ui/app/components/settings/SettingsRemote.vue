<script setup lang="ts">
// Settings > Remote: whether this machine takes sessions started elsewhere (device-local, never synced), the chat
// identities linked to this account and the user's own Telegram bot (only this account sees them), and where sessions a
// chat app starts run (synced).
import type { HostView } from '~/lib/hosts/types'
import { REMOTE_MODES, type ChatBotView, type ChatIdentityView, type ChatKind, type ChatLink, type DeliveryKind, type FolderView, type MachineView } from '~/lib/remote/types'
import type { ChatDefaults } from '~/lib/transport/types'

const store = useSessionStore()
const api = () => store.transport!.remote()

const allow = ref(false)
const policyOff = ref(false)
const chatPolicyOff = ref(false)
const identities = ref<ChatIdentityView[]>([])
const chatAvailable = ref(false)
const providers = ref<ChatKind[]>([])
// The organization's Telegram bot ('' when none: links then open the user's own bot).
const telegramBot = ref('')
const links = reactive<Partial<Record<ChatKind, ChatLink>>>({})
const machines = ref<MachineView[]>([])
const agentHosts = ref<HostView[]>([])
const folders = ref<FolderView[]>([])
const defaults = reactive<Required<ChatDefaults>>({ deviceId: '', folder: '', mode: '', agentHostId: '', repositoryKey: '' })
const error = ref('')
const saved = ref(false)

const bot = ref<ChatBotView | undefined>()
const botAllowed = ref(false)
const botForm = reactive<{ botToken: string, enabled: boolean, delivery: DeliveryKind }>({ botToken: '', enabled: true, delivery: 'auto' })
const botBusy = ref(false)
const botSaved = ref(false)

const PROVIDER_NAMES: Record<ChatKind, string> = { slack: 'Slack', telegram: 'Telegram' }
const CHAT_KINDS: ChatKind[] = ['slack', 'telegram']
// Admins set the organization's apps up here too: the button opens Chat apps.
const access = useAccess()
const modal = useSettingsModal()
const canSetUp = computed(() => access.can('policy.write'))
onMounted(() => access.load())
const botOpen = ref(false)

// The provider row's line: whether the organization's app is there to link to.
function about(p: ChatKind) {
  if (providers.value.includes(p)) return p === 'slack' ? tt('Your organization\'s Slack app') : telegramBot.value ? tt('Your organization\'s bot @{0}', [telegramBot.value]) : tt('Your organization\'s bot')
  if (p === 'telegram' && botAllowed.value) return tt('Not connected on this server; your own bot works without it')
  return canSetUp.value ? tt('Not connected on this server yet') : tt('Not connected on this server. An admin connects it under Chat apps.')
}

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
    if (prefs?.chatDefaults) Object.assign(defaults, { agentHostId: '', repositoryKey: '', ...prefs.chatDefaults })
    const ids = await api().identities()
    identities.value = ids.identities
    chatAvailable.value = ids.available
    providers.value = ids.providers
    telegramBot.value = ids.telegramBot
    const mine = await api().myBot().catch(() => ({ bot: undefined, allowed: false }))
    bot.value = mine.bot
    botAllowed.value = mine.allowed
    if (mine.bot) Object.assign(botForm, { enabled: mine.bot.enabled, delivery: mine.bot.delivery })
    machines.value = await api().machines().catch(() => [])
    agentHosts.value = await store.transport.hosts().list().catch(() => [])
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

async function newLink(provider: ChatKind, personalBot = false) {
  error.value = ''
  try {
    links[provider] = await api().startLink(provider, personalBot)
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

async function saveBot() {
  error.value = ''
  botSaved.value = false
  botBusy.value = true
  try {
    bot.value = await api().putMyBot({ botToken: botForm.botToken.trim() || undefined, enabled: botForm.enabled, delivery: botForm.delivery })
    botForm.botToken = ''
    botSaved.value = true
    botOpen.value = false
    await refresh()
  } catch (e) {
    error.value = message(e)
  } finally {
    botBusy.value = false
  }
}

async function removeBot() {
  error.value = ''
  try {
    await api().deleteMyBot()
    bot.value = undefined
    await refresh()
  } catch (e) {
    error.value = message(e)
  }
}

const machine = computed(() => machines.value.find(m => m.deviceId === defaults.deviceId))
const machineOptions = computed(() => [{ value: '', label: tt('The most recently seen machine that takes remote starts') }, ...machines.value.filter(m => m.remoteStarts || m.daemon).map(m => ({ value: m.deviceId, label: m.name || tt('Unnamed machine'), hint: m.online ? '' : tt('offline') }))])
const hostOptions = computed(() => [{ value: '', label: tt('None: one of my machines') }, ...agentHosts.value.map(h => ({ value: h.id, label: h.name, hint: h.unavailableReason || (h.online ? '' : tt('offline')) }))])
const folderOptions = computed(() => {
  const opts = [{ value: '', label: tt('Its most recent folder') }, ...folders.value.map(f => ({ value: f.path, label: f.name || f.path, hint: f.path }))]
  if (defaults.folder && !folders.value.some(f => f.path === defaults.folder)) opts.push({ value: defaults.folder, label: defaults.folder, hint: '' })
  return opts
})
const modeOptions = [{ value: '', label: tt('Default') }, ...REMOTE_MODES.filter(m => m !== 'default').map(m => ({ value: m, label: m }))]
const deliveryOptions = computed(() => [
  { value: 'auto', label: tt('Automatic'), hint: tt('A webhook when this server has a public HTTPS address, polling otherwise') },
  { value: 'webhook', label: tt('Webhook'), hint: tt('Telegram calls this server') },
  { value: 'polling', label: tt('Polling'), hint: tt('This server asks Telegram; no public address needed') }
])

watch(() => machine.value?.hostId, async (hostId) => {
  folders.value = []
  if (!hostId || !machine.value?.online || !machine.value.remoteStarts) return
  folders.value = await api().folders(hostId).catch(() => [])
}, { immediate: true })

async function saveDefaults() {
  error.value = ''
  saved.value = false
  try {
    await store.transport!.updateUserPreferences({ chatDefaults: { ...defaults, repositoryKey: defaults.agentHostId ? defaults.repositoryKey.trim() : '' } })
    saved.value = true
  } catch (e) {
    error.value = message(e)
  }
}

const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
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

    <h3 class="mt-6 mb-1 text-[13px] font-semibold text-strong">
      {{ tt('Chat apps') }}
    </h3>
    <p class="m-0 mb-3 text-[12px] text-muted">
      {{ chatPolicyOff ? tt('Your organization\'s policy turned chat apps off.') : tt('Mention the Slack app or write to the Telegram bot, and a session starts on your machine. Only you see these links.') }}
    </p>
    <div
      v-if="!chatPolicyOff"
      class="overflow-hidden border border-default"
    >
      <div
        v-for="(p, pi) in CHAT_KINDS"
        :key="p"
        class="flex flex-col"
        :class="{ 'border-t border-default': pi > 0 }"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            :text="p"
            :dim="!providers.includes(p) && !(p === 'telegram' && bot)"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-strong">
              {{ PROVIDER_NAMES[p] }}
              <span
                v-if="identities.some(i => i.provider === p)"
                class="text-[11px] text-ok"
              >{{ tt('linked') }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ about(p) }}</span>
          </span>
          <HBtn
            v-if="providers.includes(p)"
            size="sm"
            @click="newLink(p, p === 'telegram' && !telegramBot)"
          >
            {{ links[p] ? tt('New code') : tt('Link account') }}
          </HBtn>
          <HBtn
            v-if="canSetUp"
            size="sm"
            @click="modal.setSection('chat')"
          >
            {{ providers.includes(p) ? tt('Settings') : tt('Set up') }}
          </HBtn>
        </div>

        <div
          v-if="links[p] && !(p === 'telegram' && bot && links.telegram?.deepLink?.includes(bot.botUsername))"
          class="border-t border-default py-2.5 pr-3 pl-[52px] text-[11.5px] text-muted"
        >
          <template v-if="links[p]!.deepLink">
            {{ tt('Open this link within 15 minutes, or send the bot:') }}
            <a
              :href="links[p]!.deepLink"
              target="_blank"
              rel="noopener noreferrer"
              class="text-accent underline"
            >{{ links[p]!.deepLink }}</a>
            <code class="text-strong">/start {{ links[p]!.code }}</code>
          </template>
          <template v-else>
            {{ tt('Send this to the app within 15 minutes:') }} <code class="text-strong">link {{ links[p]!.code }}</code>
          </template>
        </div>

        <div
          v-for="i in identities.filter(x => x.provider === p)"
          :key="`${i.provider}/${i.teamId}/${i.externalUserId}`"
          class="flex items-center gap-3 border-t border-default py-2.5 pr-3 pl-[52px]"
        >
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="text-[12.5px] text-strong">{{ i.displayName || i.externalUserId }}</span>
            <span class="truncate text-[11.5px] text-muted">{{ i.linkedVia === 'directory' ? tt('Linked by your directory email') : tt('Linked with a code') }}</span>
          </span>
          <HBtn
            size="sm"
            tone="danger"
            @click="unlink(i)"
          >
            {{ tt('Unlink') }}
          </HBtn>
        </div>

        <template v-if="p === 'telegram' && botAllowed">
          <div class="flex items-center gap-3 border-t border-default py-2.5 pr-3 pl-[52px]">
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="flex items-center gap-2 text-[12.5px] text-strong">
                {{ bot ? `@${bot.botUsername}` : tt('Your own bot') }}
                <span
                  v-if="bot && !bot.enabled"
                  class="text-[11px] text-dimmed"
                >{{ tt('off') }}</span>
              </span>
              <span
                class="truncate text-[11.5px]"
                :class="bot?.lastError ? 'text-bad' : 'text-muted'"
              >
                <template v-if="bot?.lastError">{{ bot.lastError }}</template>
                <template v-else-if="bot">{{ tt('Updates by {0}; last update {1}', [bot.activeDelivery === 'webhook' ? tt('webhook') : tt('polling'), bot.lastEventAt ? new Date(bot.lastEventAt).toLocaleString() : tt('none yet')]) }}</template>
                <template v-else>{{ tt('Optional: a bot only you use, next to your organization\'s. Create it with @BotFather.') }}</template>
              </span>
            </span>
            <HBtn
              v-if="bot"
              size="sm"
              @click="newLink('telegram', true)"
            >
              {{ tt('Create link') }}
            </HBtn>
            <HBtn
              v-if="!botOpen"
              size="sm"
              @click="botOpen = true"
            >
              {{ bot ? tt('Change') : tt('Connect') }}
            </HBtn>
            <HBtn
              v-if="bot"
              size="sm"
              tone="danger"
              @click="removeBot"
            >
              {{ tt('Remove') }}
            </HBtn>
          </div>
          <div
            v-if="bot && links.telegram?.deepLink?.includes(bot.botUsername)"
            class="border-t border-default py-2.5 pr-3 pl-[52px] text-[11.5px] text-muted"
          >
            {{ tt('Open this link within 15 minutes:') }}
            <a
              :href="links.telegram.deepLink"
              target="_blank"
              rel="noopener noreferrer"
              class="text-accent underline"
            >{{ links.telegram.deepLink }}</a>
          </div>
          <form
            v-if="botOpen"
            :aria-label="tt('Your own Telegram bot')"
            class="flex flex-col gap-2 border-t border-default px-3 py-3 pl-[52px]"
            @submit.prevent="saveBot"
          >
            <p class="m-0 text-[11.5px] text-muted">
              {{ tt('Only you can see or change it; the token is stored encrypted and never shown again.') }}
            </p>
            <input
              v-model="botForm.botToken"
              type="password"
              autocomplete="off"
              :aria-label="tt('Your bot\'s token')"
              :placeholder="bot ? tt('Bot token (stored; leave empty to keep)') : tt('Bot token (123456:ABC-...)')"
              :class="inputClass"
            >
            <label class="flex flex-col gap-1 text-[12px]">
              <span class="text-muted">{{ tt('Updates') }}</span>
              <HSelect
                v-model="botForm.delivery"
                :options="deliveryOptions"
                :aria-label="tt('Updates')"
              />
            </label>
            <div class="flex items-center justify-between gap-4">
              <span class="set-label">
                <span>{{ tt('On') }}</span>
              </span>
              <HToggle
                v-model="botForm.enabled"
                :label="tt('On')"
              />
            </div>
            <div class="flex items-center justify-end gap-3">
              <span
                v-if="botSaved"
                role="status"
                class="text-[12px] text-ok"
              >{{ tt('Saved') }}</span>
              <HBtn
                size="md"
                @click="botOpen = false"
              >
                {{ tt('Cancel') }}
              </HBtn>
              <HBtn
                size="md"
                variant="primary"
                type="submit"
                :disabled="botBusy"
              >
                {{ botBusy ? tt('Checking the token…') : tt('Save') }}
              </HBtn>
            </div>
          </form>
        </template>
      </div>
    </div>

    <template v-if="!chatPolicyOff">
      <template v-if="chatAvailable">
        <h3 class="mt-5 mb-1 text-[12.5px] text-strong">
          {{ tt('Where chat sessions run') }}
        </h3>
        <form
          class="flex flex-col gap-2"
          @submit.prevent="saveDefaults"
        >
          <label
            v-if="agentHosts.length"
            class="flex flex-col gap-1 text-[12px]"
          >
            <span class="text-muted">{{ tt('Always-on machine') }}</span>
            <HSelect
              v-model="defaults.agentHostId"
              :options="hostOptions"
              :aria-label="tt('Always-on machine')"
            />
          </label>
          <label
            v-if="defaults.agentHostId"
            class="flex flex-col gap-1 text-[12px]"
          >
            <span class="text-muted">{{ tt('Repository') }}</span>
            <input
              v-model="defaults.repositoryKey"
              autocomplete="off"
              :aria-label="tt('Repository')"
              :placeholder="tt('host/owner/name, e.g. github.com/acme/widgets')"
              :class="inputClass"
            >
          </label>
          <template v-else>
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
          </template>
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
    </template>
  </section>
</template>
