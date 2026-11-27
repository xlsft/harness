<script setup lang="ts">
// Settings > Computer use: the switch, the security profile and what the operating system allows. Device-local: kept by
// the client of this machine and never synced, so each device of the user decides for itself. Off and Strict by default;
// the client enforces all of it, and the agent has no tool to change it. The status is only read, which never makes the
// system prompt; the user grants permissions in the system's settings.
import type { HSelectOption } from '~/components/HSelect.vue'
import { TIER_LABELS } from '~/lib/session/computer'
import { isDesktop, openExternal } from '~/lib/desktop'
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
  { key: 'browsers', label: tt('Browsers') },
  { key: 'terminals', label: tt('Terminals and IDEs') },
  { key: 'other', label: tt('Other apps') }
] as const

const SWITCHES = [
  { key: 'allowSecureTyping', label: tt('Type into secure fields'), warning: 'The agent could type into password boxes. Text it reads on screen could steer what it types there.' },
  { key: 'allowRemoteOrigin', label: tt('Work on messages from other devices'), warning: 'The agent could control this machine while nobody is here to see it or press Stop. Access cards can still only be answered here.' },
  { key: 'allowSystemShortcuts', label: tt('Press system shortcuts'), warning: 'The agent could use app switching, system search, log out and the system menu, which act outside the apps you approved.' },
  { key: 'allowSettingsEdit', label: tt('Let the agent edit these settings'), warning: 'The agent could give itself more access. While this is off, its file tools always ask you first. Its shell commands ask only when the path shows in the command: a shell can write any of your files, so this is not a hard stop.' }
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

const desktop = isDesktop()
</script>

<template>
  <SettingsRealChrome class="mb-6" />
  <section
    :aria-label="tt('Computer use')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Computer use') }}
    </h2>
    <p class="set-sub">
      {{ tt('Lets the agent see and control apps on this machine. You approve each app in a session, and can stop it at any time. These settings apply to this device only, and the agent cannot change them with its tools.') }}
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
        <span>{{ tt('Allow computer use on this device') }}</span>
      </div>
      <HToggle
        v-model="enabled"
        :label="tt('Allow computer use on this device')"
        :disabled="!status || !status.supported || saving"
      />
    </div>
    <div class="set-row">
      <div class="set-label">
        <span>{{ tt('Security profile') }}</span>
        <span class="set-desc">{{ PROFILE_NOTES[active] }}</span>
      </div>
      <div class="w-40 shrink-0">
        <HSelect
          v-model="profileChoice"
          :aria-label="tt('Security profile')"
          :options="profileOptions"
          :disabled="!status || saving"
        />
      </div>
    </div>

    <template v-if="editing">
      <h3 class="set-h mt-5 text-[13px]">
        {{ tt('What the agent may do in each kind of app') }}
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
        {{ tt('Single apps') }}
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
              :aria-label="tt('Tier of {0}', [appName(a.id)])"
              :options="tierOptions"
              :disabled="saving"
              @update:model-value="setAppTier(a.id, $event as ComputerTierName)"
            />
          </div>
          <HBtn
            :disabled="saving"
            @click="setAppTier(a.id, null)"
          >
            {{ tt('Remove') }}
          </HBtn>
        </div>
      </div>
      <div class="mt-2 w-64">
        <HSelect
          v-model="addApp"
          :aria-label="tt('Add an app')"
          :placeholder="tt('Add an app…')"
          :options="appOptions"
          :disabled="saving"
        />
      </div>

      <h3 class="set-h mt-5 text-[13px]">
        {{ tt('Blocked apps') }}
      </h3>
      <p class="set-sub">
        {{ tt('Not controllable unless you lift them here, one by one. You still approve each in a session. Harness itself stays blocked, so the agent can never reach its own access card or Stop.') }}
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
          :label="tt('Lift the block on {0}', [e.label])"
          :disabled="saving"
          @update:model-value="toggleLifted(e.id, $event)"
        />
      </div>

      <h3 class="set-h mt-5 text-[13px]">
        {{ tt('Rules') }}
      </h3>
      <p class="set-sub">
        {{ tt('All off by default.') }}
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
    <section
      v-if="status?.permissions?.length"
      :aria-label="tt('macOS permissions')"
      class="mt-6 mb-8 flex flex-col"
    >
      <div class="mb-3 flex items-end gap-3">
        <span class="flex min-w-0 flex-1 flex-col gap-1">
          <h3 class="m-0 text-[13px] font-semibold text-strong">
            {{ tt('macOS permissions') }}
          </h3>
          <span class="text-[12px] text-muted">{{ tt('What the system has to allow before the agent can see and use your apps.') }}</span>
        </span>
        <HBtn
          size="sm"
          tone="muted"
          @click="load"
        >
          <HIcon
            name="refresh"
            :size="12"
          />{{ tt('Check again') }}
        </HBtn>
      </div>
      <div class="overflow-hidden border border-default">
        <div
          v-for="(p, i) in status.permissions"
          :key="p.id"
          class="flex flex-col"
          :class="{ 'border-t border-default': i > 0 }"
        >
          <div class="flex items-center gap-3 px-3 py-2.5">
            <SettingsMonogram
              :icon="p.id === 'screen_recording' ? 'monitor' : 'cursor'"
              :dim="!p.granted"
            />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="flex items-center gap-2 text-[12.5px] text-strong">
                {{ p.name }}
                <span
                  class="text-[11px]"
                  :class="p.granted ? 'text-ok' : 'text-warn'"
                >{{ p.granted ? tt('allowed') : tt('not allowed') }}</span>
              </span>
              <span class="text-[11.5px] text-muted">{{ p.description }}</span>
            </span>
            <HBtn
              v-if="desktop && p.settingsUrl && !p.granted"
              size="sm"
              @click="openExternal(p.settingsUrl)"
            >
              <HIcon
                name="arrow-up-right"
                :size="12"
              />{{ tt('Open Settings') }}
            </HBtn>
          </div>
          <p
            v-if="!p.granted && p.howToGrant"
            class="m-0 border-t border-default bg-hover py-2 pr-3 pl-[52px] text-[11.5px] leading-relaxed text-toned"
          >
            {{ p.howToGrant }}
          </p>
        </div>
      </div>
    </section>
  </section>
</template>
