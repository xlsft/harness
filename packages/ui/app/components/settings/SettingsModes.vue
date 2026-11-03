<script setup lang="ts">
// Settings > General > Permission modes: one row per mode in the client's order. Everyone picks the mode new
// sessions start with ("default", kept in this browser with the other new-session defaults); admins also switch
// modes on or off for everyone on the server (AdminService permission mode policy, enforced by every client).
// A disabled mode is dimmed and cannot be the default; Ask can never be disabled. Everything saves at once.
import type { IconName } from '~/utils/icons'
import type { PermissionModeName } from '~/lib/session/types'

const store = useSessionStore()
const { prefs, save } = useSessionDefaults()

const ICONS: Record<PermissionModeName, IconName> = {
  default: 'question',
  accept_edits: 'edit',
  plan: 'list',
  auto: 'ai',
  dont_ask: 'stop',
  bypass: 'warn'
}

const rows = computed(() => MODE_ORDER.map(k => MODES.find(m => m.key === k) ?? DONT_ASK_MODE))
const isOff = (k: PermissionModeName) => store.disabledModes.includes(k)
// The saved default, or the first allowed mode when the policy disables it.
const defaultMode = computed(() => fitMode(prefs.value.mode ?? 'auto', store.disabledModes))

// Admins can read the policy; anyone else is refused, which is how this screen knows.
const admin = ref(false)
const saving = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

onMounted(async () => {
  if (!store.transport) return
  try {
    store.disabledModes = await store.transport.users().getModePolicy()
    admin.value = true
  } catch {
    // Not an admin (or a client too old to forward the call): the list from Initialize stays.
  }
})

async function setAllowed(k: PermissionModeName, on: boolean) {
  if (!store.transport || k === 'default') return
  const prev = store.disabledModes
  const next = on ? prev.filter(m => m !== k) : [...prev, k]
  store.disabledModes = next
  saving.value = true
  try {
    store.disabledModes = await store.transport.users().setModePolicy(next)
    error.value = ''
  } catch (e) {
    store.disabledModes = prev
    error.value = message(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section
    aria-label="Permission modes"
    class="mb-6 flex flex-col"
  >
    <h2 class="set-h">
      Permission modes
    </h2>
    <p class="set-sub">
      How much the agent may do without asking. New sessions start in the default one.
      <template v-if="admin">
        Allowed switches a mode on or off for everyone on this server.
      </template>
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <div class="border border-default">
      <div
        v-for="(m, i) in rows"
        :key="m.key"
        class="flex items-center gap-3 px-3 py-2.5"
        :class="{ 'border-t border-default': i > 0 }"
      >
        <span
          aria-hidden="true"
          class="flex size-7 shrink-0 items-center justify-center rounded-control border border-line bg-raised"
          :class="isOff(m.key) ? 'text-dimmed' : 'text-strong'"
        ><HIcon
          :name="ICONS[m.key]"
          :size="14"
        /></span>
        <span
          class="flex min-w-0 flex-1 flex-col gap-0.5"
          :class="{ 'opacity-50': isOff(m.key) }"
        >
          <span
            class="text-[12.5px]"
            :class="m.cls"
          >{{ m.label }}</span>
          <span class="truncate text-[11.5px] text-muted">{{ m.detail }}</span>
        </span>
        <span
          v-if="isOff(m.key)"
          class="text-[11px] text-dimmed"
        >Disabled by your admin</span>
        <button
          v-else
          type="button"
          role="radio"
          :aria-checked="defaultMode === m.key"
          :aria-label="`Start new sessions in ${m.label} mode`"
          class="flex shrink-0 items-center gap-1.5 text-[11px]"
          :class="defaultMode === m.key ? 'text-strong' : 'text-muted hover:text-strong'"
          @click="save('mode', m.key)"
        >
          <span
            class="flex size-3.5 items-center justify-center border"
            :class="defaultMode === m.key ? 'border-accent bg-accent' : 'border-accented'"
          ><HIcon
            v-if="defaultMode === m.key"
            name="check"
            :size="10"
            class="text-strong"
          /></span>
          default
        </button>
        <span
          v-if="admin"
          class="flex shrink-0 items-center gap-1.5 text-[11px] text-muted"
          :title="m.key === 'default' ? 'Ask is always allowed' : undefined"
        >
          allowed
          <HToggle
            :model-value="!isOff(m.key)"
            :disabled="m.key === 'default' || saving"
            :label="`Allow ${m.label} mode for everyone`"
            @update:model-value="setAllowed(m.key, $event)"
          />
        </span>
      </div>
    </div>
  </section>
</template>
