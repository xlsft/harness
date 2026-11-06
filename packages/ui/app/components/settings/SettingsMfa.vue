<script setup lang="ts">
// Settings > Account: two-factor sign-in with an authenticator app. Turning it on shows a QR code and the key, a code confirms it,
// and the recovery codes show once. It is asked for on the sign-in page, which also covers approving new devices.
import type { MfaEnrollment, MfaStatus } from '~/lib/enterprise/types'

const store = useSessionStore()

const status = ref<MfaStatus | null>(null)
const enrollment = ref<MfaEnrollment | null>(null)
const recovery = ref<string[]>([])
const code = ref('')
const manage = ref<'' | 'disable' | 'regenerate'>('')
const error = ref('')

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

const load = () => run(async () => {
  status.value = await api().mfaStatus()
})
onMounted(load)

async function start() {
  await run(async () => {
    enrollment.value = await api().startMfa()
    code.value = ''
  })
}

async function confirm() {
  if (await run(async () => {
    recovery.value = await api().confirmMfa(code.value.trim())
    enrollment.value = null
    code.value = ''
  })) await load()
}

async function act() {
  const kind = manage.value
  if (await run(async () => {
    if (kind === 'disable') await api().disableMfa(code.value.trim())
    else recovery.value = await api().regenerateRecoveryCodes(code.value.trim())
    code.value = ''
    manage.value = ''
  })) await load()
}

async function copy() {
  try {
    await navigator.clipboard.writeText(recovery.value.join('\n'))
  } catch {
    // Select and copy by hand.
  }
}

const input = 'h-8 w-[180px] border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <section aria-label="Two-factor sign-in">
    <h2 class="set-h mt-8">
      Two-factor sign-in
    </h2>
    <p class="set-sub">
      A code from an authenticator app at sign-in, and when connecting a new device. <template v-if="status?.required">
        Your organization requires it.
      </template>
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="recovery.length"
      role="status"
      class="mb-3 flex flex-col gap-2 border border-accent px-3 py-2.5 text-[12px]"
    >
      <span class="text-strong">Recovery codes</span>
      <span class="text-muted">Keep them somewhere safe. Each works once if you lose your authenticator, and they are not shown again.</span>
      <ul class="m-0 grid list-none grid-cols-2 gap-x-4 gap-y-0.5 p-0 font-mono text-[12.5px] text-strong select-all">
        <li
          v-for="c in recovery"
          :key="c"
        >
          {{ c }}
        </li>
      </ul>
      <div class="flex gap-2">
        <HBtn
          size="sm"
          @click="copy"
        >
          Copy
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          tone="muted"
          @click="recovery = []"
        >
          Done
        </HBtn>
      </div>
    </div>

    <form
      v-if="enrollment"
      class="flex flex-col gap-3 border border-default p-3"
      aria-label="Set up an authenticator"
      @submit.prevent="confirm"
    >
      <span class="text-[12px] text-toned">Scan this with your authenticator app, or type the key into it. Then enter the code it shows.</span>
      <div class="flex flex-wrap items-center gap-4">
        <!-- eslint-disable-next-line vue/no-v-html -- an SVG the server generated from the secret, no outside input -->
        <div
          class="h-[164px] w-[164px] bg-white p-2 [&>svg]:h-full [&>svg]:w-full"
          v-html="enrollment.qrSvg"
        />
        <code class="min-w-0 flex-1 font-mono text-[12.5px] break-all text-strong select-all">{{ enrollment.secret }}</code>
      </div>
      <div class="flex items-center gap-2">
        <input
          v-model="code"
          inputmode="numeric"
          autocomplete="one-time-code"
          aria-label="Code from the authenticator"
          placeholder="123456"
          required
          :class="input"
        >
        <HBtn
          size="sm"
          variant="primary"
          type="submit"
        >
          Turn on
        </HBtn>
        <HBtn
          size="sm"
          variant="ghost"
          tone="muted"
          @click="enrollment = null"
        >
          Cancel
        </HBtn>
      </div>
    </form>

    <template v-else-if="status">
      <div class="set-row">
        <div class="set-label">
          <span>{{ status.enabled ? 'On' : 'Off' }}</span>
          <span class="set-desc">
            {{ status.enabled ? `${status.recoveryCodesLeft} recovery ${status.recoveryCodesLeft === 1 ? 'code' : 'codes'} left.` : 'Only your password protects this account.' }}
          </span>
        </div>
        <div class="flex gap-2">
          <HBtn
            v-if="!status.enabled"
            size="sm"
            @click="start"
          >
            Turn on
          </HBtn>
          <template v-else>
            <HBtn
              size="sm"
              @click="manage = manage === 'regenerate' ? '' : 'regenerate'"
            >
              New recovery codes
            </HBtn>
            <HBtn
              v-if="!status.required"
              size="sm"
              tone="danger"
              @click="manage = manage === 'disable' ? '' : 'disable'"
            >
              Turn off
            </HBtn>
          </template>
        </div>
      </div>
      <form
        v-if="manage"
        class="mt-2 flex items-center gap-2"
        @submit.prevent="act"
      >
        <input
          v-model="code"
          autocomplete="one-time-code"
          :aria-label="manage === 'disable' ? 'A current code or a recovery code' : 'A current code'"
          :placeholder="manage === 'disable' ? 'Code or recovery code' : 'Current code'"
          required
          :class="input"
        >
        <HBtn
          size="sm"
          :tone="manage === 'disable' ? 'danger' : 'default'"
          type="submit"
        >
          {{ manage === 'disable' ? 'Turn off' : 'Replace codes' }}
        </HBtn>
      </form>
    </template>
  </section>
</template>
