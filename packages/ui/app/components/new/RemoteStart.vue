<script setup lang="ts">
// Start on a machine (phone dispatch, docs/design/chat.md): a new session on one of the user's other machines that is
// online and takes remote starts, in one of its recent folders, in a chosen mode. The server and that machine decide
// whether it may start; this only collects the choice and opens the session once it exists.
import { REMOTE_MODES, type FolderView, type MachineView } from '~/lib/remote/types'

const emit = defineEmits<{ close: [], started: [id: string] }>()
const store = useSessionStore()
const api = () => store.transport!.remote()

const machines = ref<MachineView[]>([])
const folders = ref<FolderView[]>([])
const hostId = ref('')
const folder = ref('')
const mode = ref('default')
const message = ref('')
const loading = ref(true)
const loadingFolders = ref(false)
const busy = ref(false)
const error = ref('')
const policyOff = ref(false)

const MODE_LABELS: Record<string, string> = {
  default: tt('Ask before edits and commands'),
  acceptEdits: tt('Accept edits'),
  plan: tt('Plan first'),
  auto: tt('Auto'),
  dontAsk: tt('Don\'t ask (deny what would ask)')
}

function messageOf(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

// A machine signed in again gets a new device, so one computer can be listed many times: keep one per name, the one that
// can take the start, else the one online, else the one seen last.
const rank = (m: MachineView) => (m.online && m.remoteStarts ? 2 : m.online ? 1 : 0)
const distinct = computed(() => {
  const best = new Map<string, MachineView>()
  for (const m of machines.value) {
    const key = m.name || m.hostId
    const had = best.get(key)
    if (!had || rank(m) > rank(had) || (rank(m) === rank(had) && m.lastSeenAt > had.lastSeenAt)) best.set(key, m)
  }
  return [...best.values()]
})
const usable = computed(() => distinct.value.filter(m => m.online && m.remoteStarts))
const others = computed(() => distinct.value.filter(m => !(m.online && m.remoteStarts)).sort((a, b) => rank(b) - rank(a) || b.lastSeenAt.localeCompare(a.lastSeenAt)))
const OTHERS_SHOWN = 3
const machineOptions = computed(() => usable.value.map(m => ({ value: m.hostId, label: m.name || tt('Unnamed machine') })))
const folderOptions = computed(() => folders.value.map(f => ({ value: f.path, label: f.name || f.path, hint: f.branch ? `${f.path} · ${f.branch}` : f.path })))
const modeOptions = computed(() => REMOTE_MODES.filter(m => !(store.disabledModes as string[]).includes(m)).map(m => ({ value: m, label: MODE_LABELS[m] ?? m })))
const canStart = computed(() => !!hostId.value && !!folder.value && !!message.value.trim() && !busy.value)

onMounted(async () => {
  try {
    policyOff.value = (await store.transport?.getUserPreferences().catch(() => undefined))?.managedPolicy?.remoteStartsDisabled ?? false
    machines.value = await api().machines()
    hostId.value = usable.value[0]?.hostId ?? ''
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loading.value = false
  }
})

watch(hostId, async (id) => {
  folders.value = []
  folder.value = ''
  if (!id) return
  loadingFolders.value = true
  error.value = ''
  try {
    folders.value = await api().folders(id)
    folder.value = folders.value[0]?.path ?? ''
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    loadingFolders.value = false
  }
})

async function start() {
  if (!canStart.value) return
  busy.value = true
  error.value = ''
  try {
    const id = await api().start({ hostId: hostId.value, folder: folder.value, mode: mode.value, message: message.value })
    message.value = ''
    emit('started', id)
  } catch (e) {
    error.value = messageOf(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <form
    :aria-label="tt('Start on a machine')"
    class="flex w-full max-w-[440px] flex-col gap-3 border border-accented bg-surface p-4 text-[12.5px]"
    @submit.prevent="start"
  >
    <div class="flex items-center gap-2">
      <HIcon
        name="phone"
        :size="14"
        class="text-muted"
      />
      <h2 class="m-0 flex-1 text-[13px] font-semibold text-strong">
        {{ tt('Start on a machine') }}
      </h2>
      <HIconBtn
        icon="close"
        :label="tt('Close')"
        @click="emit('close')"
      />
    </div>
    <p class="m-0 text-[12px] text-muted">
      {{ tt('The session runs on that machine, in one of its recent folders, and shows up here when it starts.') }}
    </p>

    <p
      v-if="policyOff"
      role="status"
      class="m-0 border border-line px-3 py-2 text-[12px] text-toned"
    >
      {{ tt('Your organization\'s policy turned remote starts off.') }}
    </p>
    <p
      v-else-if="!loading && !usable.length"
      role="status"
      class="m-0 border border-line px-3 py-2 text-[12px] text-toned"
    >
      {{ tt('None of your machines is online and taking remote starts. On the machine, turn on Settings > Remote > Allow remote starts, and keep it online.') }}
    </p>

    <template v-if="usable.length && !policyOff">
      <label class="flex flex-col gap-1">
        <span class="text-muted">{{ tt('Machine') }}</span>
        <HSelect
          v-model="hostId"
          :options="machineOptions"
          :aria-label="tt('Machine')"
        />
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">{{ tt('Folder') }}</span>
        <HSelect
          v-model="folder"
          :options="folderOptions"
          :disabled="loadingFolders || !folders.length"
          :placeholder="loadingFolders ? tt('Asking the machine…') : tt('No recent folders')"
          :aria-label="tt('Folder')"
        />
      </label>
      <label class="flex flex-col gap-1">
        <span class="text-muted">{{ tt('Mode') }}</span>
        <HSelect
          v-model="mode"
          :options="modeOptions"
          :aria-label="tt('Mode')"
        />
      </label>
      <textarea
        v-model="message"
        :aria-label="tt('What should it do?')"
        :placeholder="tt('What should it do?')"
        rows="3"
        class="resize-y border border-line bg-transparent px-2 py-1.5 text-[12.5px] text-strong outline-none focus:border-accent"
      />
    </template>

    <p
      v-if="error"
      role="alert"
      class="m-0 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="others.length"
      class="flex flex-col gap-1 text-[11.5px]"
    >
      <span class="text-muted">{{ tt('Not available now') }}</span>
      <div
        v-for="m in others.slice(0, OTHERS_SHOWN)"
        :key="m.hostId"
        class="flex items-center gap-2"
      >
        <HIcon
          name="monitor"
          :size="12"
          class="text-muted"
        />
        <span class="min-w-0 flex-1 truncate text-toned">{{ m.name || tt('Unnamed machine') }}</span>
        <span class="shrink-0 text-muted">{{ m.online ? tt('remote starts off') : tt('offline') }}</span>
      </div>
      <span
        v-if="others.length > OTHERS_SHOWN"
        class="text-muted"
      >{{ tt('and {0} more', [others.length - OTHERS_SHOWN]) }}</span>
    </div>

    <div
      v-if="usable.length && !policyOff"
      class="flex justify-end"
    >
      <HBtn
        size="md"
        variant="primary"
        type="submit"
        :disabled="!canStart"
      >
        {{ busy ? tt('Starting…') : tt('Start') }}
      </HBtn>
    </div>
  </form>
</template>
