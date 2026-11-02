<script setup lang="ts">
// Desktop sign-in: the server URL, then the device-flow code. The shell opens the verification page in the
// browser; this screen shows the code to confirm and waits. The last server is remembered.
import { cancelLogin, login, openExternal, type LoginEvent } from '~/lib/desktop'

const props = defineProps<{ error?: string }>()
const emit = defineEmits<{ signedIn: [] }>()

const LAST_SERVER = 'harness-server-url'
const LINKS = [
  { label: 'harness.xlsft.com', url: 'https://harness.xlsft.com' },
  { label: 'GitHub', url: 'https://github.com/xlsft/harness' },
  { label: 'Self-host the server', url: 'https://github.com/xlsft/harness/tree/main/deploy' }
]
const server = ref('')
const code = ref<{ uri: string, code: string } | null>(null)
const busy = ref(false)
const error = ref(props.error ?? '')
let stop: (() => void) | undefined

onMounted(() => {
  try {
    server.value = localStorage.getItem(LAST_SERVER) || 'http://localhost:3737'
  } catch {
    server.value = 'http://localhost:3737'
  }
})
onBeforeUnmount(() => stop?.())

async function start() {
  const url = server.value.trim().replace(/\/+$/, '')
  if (!url || busy.value) return
  busy.value = true
  error.value = ''
  code.value = null
  try {
    localStorage.setItem(LAST_SERVER, url)
  } catch {
    // Not remembered.
  }
  try {
    stop = await login(url, (e: LoginEvent) => {
      if (e.event === 'code') code.value = { uri: e.verification_uri, code: e.user_code }
      else if (e.event === 'error') {
        error.value = e.message
        busy.value = false
        code.value = null
      } else if (e.event === 'signed_in') {
        busy.value = false
        emit('signedIn')
      }
    })
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
    busy.value = false
  }
}

async function cancel() {
  await cancelLogin()
  stop?.()
  busy.value = false
  code.value = null
}
</script>

<template>
  <div class="flex h-svh flex-col bg-default px-5">
    <div class="flex flex-1 items-center justify-center">
      <div class="flex w-full max-w-[420px] flex-col gap-6">
        <div class="flex flex-col gap-4">
          <HWordmark class="h-[22px] w-auto self-start text-white" />
          <div class="flex flex-col gap-1.5">
            <h1 class="m-0 text-[17px] font-semibold text-white">
              Your workflow, unbounded.
            </h1>
            <p class="m-0 text-[13px] leading-[1.6] text-toned">
              The coding agent you already know, with any model and a server you own.
            </p>
          </div>
        </div>

        <form
          v-if="!code"
          class="flex flex-col gap-3"
          @submit.prevent="start"
        >
          <label class="flex flex-col gap-1.5 text-[12px] text-muted">
            Server
            <input
              v-model="server"
              type="url"
              spellcheck="false"
              autocomplete="url"
              aria-label="Server URL"
              placeholder="https://harness.example.com"
              class="h-9 border border-accented bg-surface px-2.5 text-[12.5px] text-white outline-none placeholder:text-dimmed focus:border-accent"
            >
          </label>
          <button
            type="submit"
            :disabled="busy || !server.trim()"
            class="h-9 bg-accent text-[12.5px] text-white disabled:opacity-50"
          >
            {{ busy ? 'Connecting…' : 'Sign in' }}
          </button>
        </form>

        <div
          v-else
          class="flex flex-col gap-3 border border-accented bg-surface p-4"
        >
          <span class="text-[12px] text-muted">Confirm this code in your browser</span>
          <span class="text-center text-[24px] tracking-[0.2em] text-white">{{ code.code }}</span>
          <span class="inline-flex items-center gap-2 text-[12px] text-muted">
            <HIcon
              name="loading"
              :size="13"
              spin
              class="text-accent"
            />Waiting…
          </span>
          <div class="flex gap-2">
            <button
              type="button"
              class="h-8 flex-1 border border-default text-[12px] text-toned hover:text-white"
              @click="openExternal(code.uri)"
            >
              Open the page again
            </button>
            <button
              type="button"
              class="h-8 flex-1 border border-default text-[12px] text-toned hover:text-white"
              @click="cancel"
            >
              Cancel
            </button>
          </div>
        </div>

        <p
          v-if="error"
          class="m-0 border border-bad px-2.5 py-1.5 text-[12px] text-bad"
        >
          {{ error }}
        </p>
      </div>
    </div>

    <footer class="flex items-center justify-center gap-4 py-4 text-[11.5px] text-muted">
      <button
        v-for="l in LINKS"
        :key="l.url"
        type="button"
        class="inline-flex items-center gap-1 hover:text-white"
        @click="openExternal(l.url)"
      >
        {{ l.label }}<HIcon
          name="arrow-up-right"
          :size="11"
        />
      </button>
    </footer>
  </div>
</template>
