<script setup lang="ts">
// Settings > Chat apps (admins): the Slack app people mention to start sessions on their machines, then the Telegram bot
// (SettingsChatTelegram). The bot token and signing secret go to the server, which checks the token, stores both
// encrypted and never sends them back.
import type { ChatAppView } from '~/lib/remote/types'

const store = useSessionStore()
const api = () => store.transport!.remote()

const app = ref<ChatAppView | null>(null)
const form = reactive({ botToken: '', signingSecret: '', enabled: true, matchByEmail: true })
const error = ref('')
const saved = ref(false)
const busy = ref(false)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function refresh() {
  error.value = ''
  try {
    app.value = await api().chatApp('slack')
    form.enabled = app.value.configured ? app.value.enabled : true
    form.matchByEmail = app.value.matchByEmail
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
    const opt = (s: string) => s.trim() || undefined
    app.value = await api().putChatApp({ provider: 'slack', botToken: opt(form.botToken), signingSecret: opt(form.signingSecret), enabled: form.enabled, matchByEmail: form.matchByEmail })
    form.botToken = ''
    form.signingSecret = ''
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
    await api().deleteChatApp('slack')
    await refresh()
  } catch (e) {
    error.value = message(e)
  }
}

const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <section
    :aria-label="tt('Chat apps')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Chat apps') }}
    </h2>
    <p class="set-sub">
      {{ tt('People mention the Slack app or write to the Telegram bot, and a session starts on one of their own computers or an always-on machine. The thread or chat gets progress, permission questions and the answer.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <p
      v-if="app?.disabledByPolicy"
      role="status"
      class="m-0 mb-3 border border-line px-3 py-2 text-[12px] text-toned"
    >
      {{ tt('The managed policy turns chat apps off: nothing starts until it allows them again.') }}
    </p>

    <template v-if="app">
      <div class="set-row">
        <span class="set-label">
          <span>{{ tt('Slack') }}</span>
          <span class="set-desc">
            <template v-if="app.configured">{{ tt('Connected to {0} ({1}); last event {2}', [app.teamName || app.teamId, app.teamId, app.lastEventAt ? new Date(app.lastEventAt).toLocaleString() : tt('none yet')]) }}</template>
            <template v-else>{{ tt('Not connected') }}</template>
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
      <div class="set-row">
        <span class="set-label">
          <span>{{ tt('Request URL') }}</span>
          <span class="set-desc">{{ tt('Use it for Event Subscriptions (app_mention, message.im, message.channels) and Interactivity. Scopes: app_mentions:read, chat:write, im:history, channels:history, users:read, users:read.email.') }}</span>
        </span>
      </div>
      <code class="mb-3 block break-all border border-line px-2 py-1 text-[12px] text-strong">{{ app.requestUrl }}</code>

      <form
        class="flex flex-col gap-2"
        @submit.prevent="save"
      >
        <input
          v-model="form.botToken"
          type="password"
          autocomplete="off"
          :aria-label="tt('Bot token')"
          :placeholder="app.configured ? tt('Bot token (stored; leave empty to keep)') : tt('Bot token (xoxb-...)')"
          :class="inputClass"
        >
        <input
          v-model="form.signingSecret"
          type="password"
          autocomplete="off"
          :aria-label="tt('Signing secret')"
          :placeholder="app.configured ? tt('Signing secret (stored; leave empty to keep)') : tt('Signing secret')"
          :class="inputClass"
        >
        <div class="flex items-center justify-between gap-4">
          <span class="set-label">
            <span>{{ tt('On') }}</span>
            <span class="set-desc">{{ tt('Off keeps the app connected and answers people with a short notice.') }}</span>
          </span>
          <HToggle
            v-model="form.enabled"
            :label="tt('On')"
          />
        </div>
        <div class="flex items-center justify-between gap-4">
          <span class="set-label">
            <span>{{ tt('Link by email') }}</span>
            <span class="set-desc">{{ tt('A Slack user whose profile email matches an account from the directory, or with a verified email, is linked to it. Otherwise people link with a code from Settings > Remote.') }}</span>
          </span>
          <HToggle
            v-model="form.matchByEmail"
            :label="tt('Link by email')"
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
    <SettingsChatTelegram />
  </section>
</template>
