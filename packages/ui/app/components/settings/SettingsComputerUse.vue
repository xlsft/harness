<script setup lang="ts">
// Settings > Computer use: the switch, the security profile and what the operating system allows. Device-local: kept by
// the client of this machine and never synced, so each device of the user decides for itself. Off and Strict by default;
// the client enforces all of it, and the agent has no tool to change it. The status is only read, which never makes the
// system prompt; the user grants permissions in the system's settings.
import type { HSelectOption } from '~/components/HSelect.vue'
import { TIER_LABELS } from '~/lib/session/computer'
import type { ComputerProfileVM, ComputerTierName } from '~/lib/session/types'
import type { ComputerUseAppVM, ComputerUseStatusVM } from '~/lib/transport/types'

const store = useSessionStore()
const status = ref<ComputerUseStatusVM | null>(null)
const error = ref('')
const saving = ref(false)
const apps = ref<ComputerUseAppVM[]>([])
const addApp = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load() {
  if (!store.transport) return
  try {
    status.value = await store.transport.getComputerUseStatus()
    error.value = ''
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(load)

async function run(f: () => Promise<ComputerUseStatusVM> | undefined) {
  if (saving.value) return
  saving.value = true
  try {
    const s = await f()
    if (s) status.value = s
    error.value = ''
  } catch (e) {
    error.value = message(e)
  } finally {
    saving.value = false
  }
}

const enabled = computed({
  get: () => status.value?.enabled ?? false,
  set: v => void run(() => store.transport?.setComputerUseEnabled(v))
})

const PROFILE_NOTES: Record<string, string> = {
  strict: 'Browsers are read-only. Terminals and IDEs take clicks only. Everything else stays blocked.',
  standard: 'Browsers can be clicked and typed in. Terminals and IDEs take clicks only. Everything else stays blocked.',
  full: 'Full control of every app you approve, terminals included, so typed text can run commands. Password managers, system settings and the other rules stay blocked.',
  custom: 'Your own choices, below. Anything you lift here applies at once.'
}

const active = computed(() => status.value?.activeProfileId ?? 'strict')
const profileOptions = computed<HSelectOption[]>(() => (status.value?.profiles ?? []).map(p => ({ value: p.id, label: p.name })))
const profileChoice = computed({
  get: () => active.value,
  set: v => void run(() => store.transport?.setComputerUseProfile(v))
})
const custom = computed(() => status.value?.profiles.find(p => p.id === 'custom') ?? null)
const editing = computed(() => active.value === 'custom' && custom.value)

const TIERS: ComputerTierName[] = ['blocked', 'read', 'click', 'full']
const tierOptions: HSelectOption[] = TIERS.map(t => ({ value: t, label: TIER_LABELS[t] }))
const KINDS = [
  { key: 'browsers', label: 'Browsers' },
  { key: 'terminals', label: 'Terminals and IDEs' },
  { key: 'other', label: 'Other apps' }
] as const

const SWITCHES = [
  { key: 'allowSecureTyping', label: 'Type into secure fields', warning: 'The agent could type into password boxes. Text it reads on screen could steer what it types there.' },
  { key: 'allowRemoteOrigin', label: 'Work on messages from other devices', warning: 'The agent could control this machine while nobody is here to see it or press Stop. Access cards can still only be answered here.' },
  { key: 'allowSystemShortcuts', label: 'Press system shortcuts', warning: 'The agent could use app switching, system search, log out and the system menu, which act outside the apps you approved.' },
  { key: 'allowSettingsEdit', label: 'Let the agent edit these settings', warning: 'The agent could give itself more access. While this is off, its file tools always ask you first. Its shell commands ask only when the path shows in the command: a shell can write any of your files, so this is not a hard stop.' }
] as const

function saveCustom(patch: Partial<ComputerProfileVM>) {
  const c = custom.value
  if (!c) return
  void run(() => store.transport?.setComputerUseProfile('custom', { ...c, ...patch }))
}

function toggleLifted(id: string, on: boolean) {
  const lifted = custom.value?.lifted ?? []
  saveCustom({ lifted: on ? [...lifted, id] : lifted.filter(x => x !== id) })
}

function setAppTier(id: string, tier: ComputerTierName | null) {
  const list = (custom.value?.apps ?? []).filter(a => a.id !== id)
  saveCustom({ apps: tier ? [...list, { id, name: appName(id), tier }] : list })
}

function appName(id: string) {
  return apps.value.find(a => a.id === id)?.name || id
}

watch(editing, async (on) => {
  if (!on || apps.value.length || !store.transport) return
  try {
    apps.value = await store.transport.listComputerUseApps()
  } catch (e) {
    error.value = message(e)
  }
}, { immediate: true })

const appOptions = computed<HSelectOption[]>(() => {
  const taken = new Set(custom.value?.apps.map(a => a.id))
  return apps.value.filter(a => !taken.has(a.id)).map(a => ({ value: a.id, label: a.name, hint: TIER_LABELS[a.tier] }))
})
watch(addApp, (id) => {
  if (!id) return
  setAppTier(id, 'read')
  addApp.value = ''
})
</script>

<template>
  <section
    aria-label="Computer use"
    class="flex flex-col"
  >
    <h2 class="set-h">
      Computer use
    </h2>
    <p class="set-sub">
      Lets the agent see and control apps on this machine. You approve each app in a session, and can stop it at any time. These settings apply to this device only, and the agent cannot change them with its tools.
    </p>
    <p
      v-if="error"
      role="alert"
      class="mb-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <div class="set-row">
      <div class="set-label">
        <span>Allow computer use on this device</span>
      </div>
      <HToggle
        v-model="enabled"
        label="Allow computer use on this device"
        :disabled="!status || !status.supported || saving"
      />
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>Security profile</span>
        <span class="set-desc">{{ PROFILE_NOTES[active] }}</span>
      </div>
      <div class="w-40 shrink-0">
        <HSelect
          v-model="profileChoice"
          aria-label="Security profile"
          :options="profileOptions"
          :disabled="!status || saving"
        />
      </div>
    </div>

    <template v-if="editing">
      <h3 class="set-h mt-5 text-[13px]">
        What the agent may do in each kind of app
      </h3>
      <div
        v-for="k in KINDS"
        :key="k.key"
        class="set-row"
      >
        <div class="set-label">
          <span>{{ k.label }}</span>
        </div>
        <div class="w-40 shrink-0">
          <HSelect
            :model-value="custom![k.key]"
            :aria-label="k.label"
            :options="tierOptions"
            :disabled="saving"
            @update:model-value="saveCustom({ [k.key]: $event as ComputerTierName })"
          />
        </div>
      </div>

      <h3 class="set-h mt-5 text-[13px]">
        Single apps
      </h3>
      <div
        v-for="a in custom!.apps"
        :key="a.id"
        class="set-row"
      >
        <div class="set-label">
          <span>{{ appName(a.id) }}</span>
          <span class="set-desc">{{ a.id }}</span>
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <div class="w-40">
            <HSelect
              :model-value="a.tier"
              :aria-label="`Tier of ${appName(a.id)}`"
              :options="tierOptions"
              :disabled="saving"
              @update:model-value="setAppTier(a.id, $event as ComputerTierName)"
            />
          </div>
          <HBtn
            :disabled="saving"
            @click="setAppTier(a.id, null)"
          >
            Remove
          </HBtn>
        </div>
      </div>
      <div class="mt-2 w-64">
        <HSelect
          v-model="addApp"
          aria-label="Add an app"
          placeholder="Add an app…"
          :options="appOptions"
          :disabled="saving"
        />
      </div>

      <h3 class="set-h mt-5 text-[13px]">
        Blocked apps
      </h3>
      <p class="set-sub">
        Not controllable unless you lift them here, one by one. You still approve each in a session. Harness itself stays blocked, so the agent can never reach its own access card or Stop.
      </p>
      <div
        v-for="e in status!.blockEntries"
        :key="e.id"
        class="set-row"
      >
        <div class="set-label">
          <span>{{ e.label }}</span>
          <span class="set-desc text-warn">{{ e.warning }}</span>
        </div>
        <HToggle
          :model-value="custom!.lifted.includes(e.id)"
          :label="`Lift the block on ${e.label}`"
          :disabled="saving"
          @update:model-value="toggleLifted(e.id, $event)"
        />
      </div>

      <h3 class="set-h mt-5 text-[13px]">
        Rules
      </h3>
      <p class="set-sub">
        All off by default.
      </p>
      <div
        v-for="s in SWITCHES"
        :key="s.key"
        class="set-row"
      >
        <div class="set-label">
          <span>{{ s.label }}</span>
          <span class="set-desc text-warn">{{ s.warning }}</span>
        </div>
        <HToggle
          :model-value="custom![s.key]"
          :label="s.label"
          :disabled="saving"
          @update:model-value="saveCustom({ [s.key]: $event })"
        />
      </div>
    </template>

    <p
      v-if="status?.unavailableReason"
      class="mt-2 text-[12px] text-muted"
    >
      {{ status.unavailableReason }}
    </p>
    <div
      v-for="p in status?.permissions ?? []"
      :key="p.id"
      class="set-row"
    >
      <div class="set-label">
        <span>{{ p.name }}</span>
        <span
          v-if="!p.granted"
          class="set-desc"
        >{{ p.howToGrant }}</span>
      </div>
      <span
        class="shrink-0 text-[12px] whitespace-nowrap"
        :class="p.granted ? 'text-ok' : 'text-warn'"
      >{{ p.granted ? 'Allowed' : 'Not allowed' }}</span>
    </div>
    <div
      v-if="status?.permissions?.length"
      class="mt-2"
    >
      <HBtn @click="load">
        Check again
      </HBtn>
    </div>
  </section>
</template>
