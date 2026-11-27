<script setup lang="ts">
// Above the composer: recording a demo into a skill (docs/design/demo-recording.md). The tools menu's "Record a demo"
// opens the picker here: the user ticks the apps the recording may watch (tiers from the active security profile;
// blocked apps cannot be picked) and starts it. While it runs this is the indicator with Stop and Discard; the client
// also shows a panel on the screen with its own Stop and a hotkey. Only the user starts or stops a recording, and only
// from this machine. Stopping hands the recording to the agent, which drafts the skill for review.
import { TIER_LABELS } from '~/lib/session/computer'
import { hotkeyKeys } from '~/lib/session/demo'
import type { SessionState } from '~/lib/session/types'
import type { DemoRecordingAppVM } from '~/lib/transport/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const toast = useToast()
const picker = useDemoRecordingPicker()

const id = computed(() => props.session.meta.id)
const state = computed(() => props.session.demoRecording)
const recording = computed(() => !!state.value?.recording)
const picking = computed(() => picker.isOpen(id.value) && !recording.value)

const apps = ref<DemoRecordingAppVM[]>([])
const unavailable = ref('')
const loading = ref(false)
const chosen = ref<Set<string>>(new Set())
const busy = ref(false)

async function load() {
  loading.value = true
  unavailable.value = ''
  try {
    const r = await store.transport?.listDemoRecordingApps?.()
    apps.value = r?.apps ?? []
    unavailable.value = r ? r.unavailable : tt('Recording a demo is not available here.')
    chosen.value = new Set()
  } catch (e) {
    unavailable.value = errorText(e)
  } finally {
    loading.value = false
  }
}

watch(picking, (v) => {
  if (v) load()
}, { immediate: true })

function errorText(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function toggle(appId: string, on: boolean) {
  const next = new Set(chosen.value)
  if (on) next.add(appId)
  else next.delete(appId)
  chosen.value = next
}

async function start() {
  if (busy.value || chosen.value.size === 0) return
  busy.value = true
  try {
    await store.startDemoRecording(id.value, [...chosen.value])
    picker.close(id.value)
  } catch (e) {
    toast.add({ title: tt('Could not start recording'), description: errorText(e), color: 'error' })
  } finally {
    busy.value = false
  }
}

async function stop(discard: boolean) {
  if (busy.value) return
  busy.value = true
  try {
    await store.stopDemoRecording(id.value, discard)
  } catch (e) {
    toast.add({ title: tt('Could not stop recording'), description: errorText(e), color: 'error' })
  } finally {
    busy.value = false
  }
}

const watched = computed(() => state.value?.apps.map(a => a.name).join(', ') ?? '')
</script>

<template>
  <div
    v-if="recording"
    class="flex items-center gap-[9px] border border-l-2 border-bad border-l-bad px-2.5 py-1.5 text-[12px]"
    role="status"
  >
    <span
      class="size-2 shrink-0 bg-bad"
      aria-hidden="true"
    />
    <span class="flex min-w-0 flex-1 flex-col">
      <span class="text-strong">{{ tt('Recording a demo · {0} steps', [String(state!.steps)]) }}</span>
      <span class="truncate text-[11.5px] text-muted">{{ state!.lastStep || watched }}</span>
    </span>
    <HKeys
      v-if="state!.hotkey"
      :keys="hotkeyKeys(state!.hotkey)"
      :title="tt('Stops the recording from any app')"
    />
    <HBtn
      :disabled="busy"
      @click="stop(true)"
    >
      {{ tt('Discard') }}
    </HBtn>
    <HBtn
      variant="primary"
      :disabled="busy"
      @click="stop(false)"
    >
      {{ tt('Stop') }}
    </HBtn>
  </div>
  <div
    v-else-if="picking"
    class="border border-accent bg-surface text-[12.5px]"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2">
      <HIcon
        name="monitor"
        class="text-accent"
      />
      <span class="text-strong">{{ tt('Record a demo') }}</span>
      <span class="flex-1" />
      <PaneBtn
        icon="close"
        :label="tt('Cancel')"
        @click="picker.close(id)"
      />
    </div>
    <div class="flex flex-col gap-2.5 px-3 py-2.5">
      <span class="text-toned">{{ tt('Show the agent a workflow once; it drafts a skill from it for you to review. Pick the apps to record: nothing else is seen.') }}</span>
      <HLoader v-if="loading" />
      <p
        v-else-if="unavailable"
        role="alert"
        class="m-0 text-[12px] text-bad"
      >
        {{ unavailable }}
      </p>
      <div
        v-else
        class="flex max-h-[220px] flex-col gap-2 overflow-y-auto"
      >
        <label
          v-for="a in apps"
          :key="a.id"
          class="flex items-start gap-2"
          :class="a.tier === 'blocked' ? 'cursor-not-allowed opacity-60' : 'cursor-pointer'"
        >
          <input
            type="checkbox"
            class="mt-[3px] accent-(--color-accent)"
            :disabled="a.tier === 'blocked'"
            :checked="chosen.has(a.id)"
            @change="toggle(a.id, ($event.target as HTMLInputElement).checked)"
          >
          <span class="flex min-w-0 flex-col">
            <span class="flex flex-wrap items-baseline gap-x-2">
              <span class="text-strong">{{ a.name }}</span>
              <span class="text-[11.5px] text-muted">{{ TIER_LABELS[a.tier] }}</span>
            </span>
            <span
              v-if="a.note"
              class="text-[11.5px] text-muted"
            >{{ a.note }}</span>
          </span>
        </label>
      </div>
      <span class="text-[11.5px] text-muted">{{ tt('Password fields are blanked and their keys are not recorded. Screenshots stay on this machine.') }}</span>
      <div class="flex flex-wrap gap-2">
        <HBtn
          variant="primary"
          size="lg"
          :disabled="busy || loading || !!unavailable || chosen.size === 0"
          @click="start"
        >
          {{ tt('Start recording') }}
        </HBtn>
        <HBtn
          size="lg"
          @click="picker.close(id)"
        >
          {{ tt('Cancel') }}
        </HBtn>
      </div>
    </div>
  </div>
</template>
