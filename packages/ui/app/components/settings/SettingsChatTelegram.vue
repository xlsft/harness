<script setup lang="ts">
// Settings > Chat apps, Telegram (admins): the organization's bot. The token goes to the server, which checks it with
// Telegram, stores it encrypted and never sends it back; the server also makes the webhook's secret token itself.
import type { ChatAppView, DeliveryKind } from '~/lib/remote/types'

const store = useSessionStore()
const api = () => store.transport!.remote()

const app = ref<ChatAppView | null>(null)
const form = reactive<{ botToken: string, enabled: boolean, delivery: DeliveryKind }>({ botToken: '', enabled: true, delivery: 'auto' })
const error = ref('')
const saved = ref(false)
const busy = ref(false)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function refresh() {
  error.value = ''
  try {
    app.value = await api().chatApp('telegram')
    form.enabled = app.value.configured ? app.value.enabled : true
    form.delivery = app.value.delivery
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(refresh)

async function save() {
  error.value = ''
  saved.value = false
  busy.value = true
  try {
    app.value = await api().putChatApp({ provider: 'telegram', botToken: form.botToken.trim() || undefined, enabled: form.enabled, matchByEmail: false, delivery: form.delivery })
    form.botToken = ''
    saved.value = true
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function remove() {
  error.value = ''
  try {
    await api().deleteChatApp('telegram')
    await refresh()
  } catch (e) {
    error.value = message(e)
  }
}

const deliveryOptions = computed(() => [
  { value: 'auto', label: tt('Automatic'), hint: tt('A webhook when this server has a public HTTPS address, polling otherwise') },
  { value: 'webhook', label: tt('Webhook'), hint: tt('Telegram calls this server') },
  { value: 'polling', label: tt('Polling'), hint: tt('This server asks Telegram; no public address needed') }
])
const activeLabel = computed(() => app.value?.activeDelivery === 'webhook' ? tt('webhook') : tt('polling'))

const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div class="mt-6 flex flex-col">
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <template v-if="app">
      <div class="set-row">
        <span class="set-label">
          <span>{{ tt('Telegram') }}</span>
          <span class="set-desc">
            <template v-if="app.configured">{{ tt('Connected to @{0} by {1}; last update {2}', [app.teamName, activeLabel, app.lastEventAt ? new Date(app.lastEventAt).toLocaleString() : tt('none yet')]) }}</template>
            <template v-else>{{ tt('Not connected. Create a bot with @BotFather and paste its token. People link their chat with a code from Settings > Remote, and only private chats are served.') }}</template>
          </span>
        </span>
        <HBtn
          v-if="app.configured"
          size="sm"
          tone="danger"
          @click="remove"
        >
          {{ tt('Remove') }}
        </HBtn>
      </div>
      <p
        v-if="app.lastError"
        class="m-0 text-[12px] text-bad"
      >
        {{ app.lastError }}
      </p>
      <template v-if="app.configured && app.activeDelivery === 'webhook'">
        <div class="set-row">
          <span class="set-label">
            <span>{{ tt('Webhook') }}</span>
            <span class="set-desc">{{ tt('Set on Telegram by this server, with a secret token it checks on every update.') }}</span>
          </span>
        </div>
        <code class="mb-3 block break-all border border-line px-2 py-1 text-[12px] text-strong">{{ app.requestUrl }}</code>
      </template>

      <form
        class="flex flex-col gap-2"
        @submit.prevent="save"
      >
        <input
          v-model="form.botToken"
          type="password"
          autocomplete="off"
          :aria-label="tt('Telegram bot token')"
          :placeholder="app.configured ? tt('Bot token (stored; leave empty to keep)') : tt('Bot token (123456:ABC-...)')"
          :class="inputClass"
        >
        <label class="flex flex-col gap-1 text-[12px]">
          <span class="text-muted">{{ tt('Updates') }}</span>
          <HSelect
            v-model="form.delivery"
            :options="deliveryOptions"
            :aria-label="tt('Updates')"
          />
        </label>
        <div class="flex items-center justify-between gap-4">
          <span class="set-label">
            <span>{{ tt('On') }}</span>
            <span class="set-desc">{{ tt('Off keeps the bot connected and answers people with a short notice.') }}</span>
          </span>
          <HToggle
            v-model="form.enabled"
            :label="tt('On')"
          />
        </div>
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
            :disabled="busy"
          >
            {{ busy ? tt('Checking the token…') : tt('Save') }}
          </HBtn>
        </div>
      </form>
    </template>
  </div>
</template>
