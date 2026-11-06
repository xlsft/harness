<script setup lang="ts">
// Connecting this window to a saved SSH host. First the host's key: a new one is shown and must be accepted, a changed one stops
// everything until the user deliberately forgets the old one. Then the client's stages, with the code to confirm when the
// host's client has to sign in. All the work is in the local client; ready reloads the window onto the host.
import type { SshHostVM, SshKeyCheck, SshStage } from '~/lib/ssh'

const props = defineProps<{ host: SshHostVM }>()
const emit = defineEmits<{ close: [] }>()
const store = useSessionStore()

const check = ref<SshKeyCheck | null>(null)
const stages = ref<SshStage[]>([])
const code = ref<SshStage | null>(null)
const error = ref('')
const busy = ref(false)
const confirmForget = ref(false)
let abort: AbortController | null = null

const api = () => store.sshApi!
const message = (e: unknown) => e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
const title = computed(() => props.host.name || props.host.destination)

async function verify() {
  error.value = ''
  check.value = null
  busy.value = true
  try {
    check.value = await api().checkKey(props.host.id)
    if (check.value.status === 'verified') await connect()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function trust() {
  if (!check.value) return
  busy.value = true
  error.value = ''
  try {
    await api().trustKey(props.host.id, check.value.fingerprint)
    await connect()
  } catch (e) {
    error.value = message(e)
    await verify()
  } finally {
    busy.value = false
  }
}

async function forgetAndCheck() {
  busy.value = true
  error.value = ''
  confirmForget.value = false
  try {
    await api().forgetKey(props.host.id)
    await verify()
  } catch (e) {
    error.value = message(e)
    busy.value = false
  }
}

async function connect() {
  stages.value = []
  code.value = null
  abort = new AbortController()
  const conn = await api().connect(props.host.id, window.location.origin, (s) => {
    stages.value = [...stages.value, s]
    if (s.userCode) code.value = s
    else if (s.stage !== 'sign_in') code.value = null
  }, abort.signal)
  store.enterSshHost({ hostId: conn.hostId, name: title.value })
}

function cancel() {
  abort?.abort()
  emit('close')
}

onMounted(verify)
onBeforeUnmount(() => abort?.abort())
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-40 flex items-center justify-center bg-ground/60 p-6"
      @pointerdown.self="cancel"
      @keydown.esc="cancel"
    >
      <div
        role="dialog"
        aria-modal="true"
        :aria-label="`Connect to ${title}`"
        class="flex max-h-full w-[min(520px,100%)] flex-col gap-3 overflow-auto rounded-panel border border-line-strong bg-raised p-5 text-[12.5px]"
      >
        <h2 class="m-0 text-[14px] font-semibold text-strong">
          Connect to {{ title }}
        </h2>

        <div
          v-if="check?.status === 'unknown'"
          class="flex flex-col gap-2"
          role="group"
          aria-label="New host key"
        >
          <p class="m-0 text-toned">
            This host has not been seen before. Compare its key with what the host's owner gives you before going on: a wrong key means someone may be in between.
          </p>
          <code
            v-for="f in check.fingerprints"
            :key="f"
            class="break-all bg-surface px-2 py-1 font-mono text-[12px] text-strong"
          >{{ f }}</code>
          <div class="flex gap-2">
            <HBtn
              variant="primary"
              :disabled="busy"
              @click="trust"
            >
              The key is right, connect
            </HBtn>
            <HBtn
              :disabled="busy"
              @click="cancel"
            >
              Cancel
            </HBtn>
          </div>
        </div>

        <div
          v-else-if="check?.status === 'changed'"
          class="flex flex-col gap-2"
          role="alert"
        >
          <p class="m-0 font-medium text-bad">
            This host's key has changed.
          </p>
          <p class="m-0 text-toned">
            Either the machine was reinstalled, or someone is intercepting the connection. Nothing was sent to it. Only go on if you know the reason. A key kept in your own ~/.ssh/known_hosts has to be removed there.
          </p>
          <div class="flex gap-2">
            <HBtn
              v-if="!confirmForget"
              :disabled="busy"
              @click="confirmForget = true"
            >
              Forget the old key…
            </HBtn>
            <HBtn
              v-else
              tone="danger"
              :disabled="busy"
              @click="forgetAndCheck"
            >
              Yes, forget it and check again
            </HBtn>
            <HBtn @click="cancel">
              Cancel
            </HBtn>
          </div>
        </div>

        <p
          v-else-if="check?.status === 'unreachable'"
          class="m-0 text-bad"
          role="alert"
        >
          {{ check.error || 'The host did not answer.' }}
        </p>

        <ol
          v-if="stages.length"
          class="m-0 flex list-none flex-col gap-1 p-0 text-muted"
          aria-live="polite"
        >
          <li
            v-for="(s, i) in stages"
            :key="i"
          >
            {{ s.message }}
          </li>
        </ol>

        <div
          v-if="code"
          class="flex flex-col gap-1 border border-accent p-3"
          role="status"
        >
          <span class="text-toned">Confirm this code to sign the client on that machine in:</span>
          <code class="font-mono text-[14px] text-strong">{{ code.userCode }}</code>
          <SettingsLink :href="code.verificationUri">
            Open the sign-in page
          </SettingsLink>
        </div>

        <p
          v-if="error"
          class="m-0 text-bad"
          role="alert"
        >
          {{ error }}
        </p>

        <div class="flex justify-end">
          <HBtn @click="cancel">
            {{ stages.length ? 'Stop' : 'Close' }}
          </HBtn>
        </div>
      </div>
    </div>
  </Teleport>
</template>
