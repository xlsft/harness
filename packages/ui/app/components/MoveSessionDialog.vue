<script setup lang="ts">
// Moving a session to an always-on host (docs/design/hosts.md, decisions 21 to 27). The client says what would go and why
// it can't; nothing moves until the user presses Move. Opened from the session menu or /move.
import type { HostView, MoveCheck } from '~/lib/hosts/types'
import type { PermissionModeName } from '~/lib/session/types'

const sessionId = useMoveSession()
const store = useSessionStore()
const toast = useToast()

const open = computed({
  get: () => !!sessionId.value,
  set: (v) => {
    if (!v) sessionId.value = ''
  }
})
const session = computed(() => store.sessions[sessionId.value])

const hosts = ref<HostView[]>([])
const host = ref('')
const check = ref<MoveCheck | null>(null)
const checking = ref(false)
const moving = ref(false)
const error = ref('')
const message = ref('')
const openPullRequest = ref(false)
const stopTurn = ref(false)

function errorText(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

const options = computed(() => hosts.value.map(h => ({ value: h.id, label: h.name, hint: h.unavailableReason || (h.online ? '' : tt('offline')) })))

async function load() {
  error.value = ''
  check.value = null
  message.value = ''
  stopTurn.value = false
  openPullRequest.value = false
  try {
    hosts.value = await store.transport!.hosts().list()
  } catch (e) {
    hosts.value = []
    error.value = errorText(e)
    return
  }
  const usable = hosts.value.find(h => !h.unavailableReason) ?? hosts.value[0]
  host.value = usable?.id ?? ''
  if (!host.value) error.value = tt('You have no always-on machine. Add one in Settings → Always-on machines.')
}

async function runCheck() {
  if (!sessionId.value || !host.value) return
  checking.value = true
  error.value = ''
  try {
    check.value = await store.transport!.hosts().checkMove(sessionId.value, host.value)
  } catch (e) {
    check.value = null
    error.value = errorText(e)
  } finally {
    checking.value = false
  }
}

watch(sessionId, (id) => {
  if (id) void load()
})
watch(host, () => void runCheck())

const canMove = computed(() => !!check.value?.ok && !moving.value && (!check.value.working || stopTurn.value))

async function move() {
  if (!canMove.value) return
  moving.value = true
  error.value = ''
  try {
    const moved = await store.transport!.hosts().move(sessionId.value, host.value, { stopTurn: stopTurn.value, message: message.value.trim(), openPullRequest: openPullRequest.value })
    toast.add({ title: tt('Moved to {0}', [moved.hostName]), description: tt('It goes on there, on {0}. This copy is read-only.', [moved.branch]) })
    sessionId.value = ''
  } catch (e) {
    error.value = errorText(e)
  } finally {
    moving.value = false
  }
}

const modeLabel = computed(() => MODE_LABEL[(check.value?.permissionMode ?? 'default') as PermissionModeName] ?? check.value?.permissionMode)

const facts = computed(() => {
  const c = check.value
  if (!c || !c.branch) return ''
  return tt('{0} from {1} in {2}: {3} commits and {4} changed files go up', [c.branch, c.base, c.repositoryKey, c.commits, c.uncommittedFiles])
})
</script>

<template>
  <UModal
    v-model:open="open"
    :title="tt('Move to an always-on machine')"
    :description="session ? session.meta.title || tt('Untitled session') : ''"
  >
    <template #body>
      <div
        class="flex flex-col gap-3 text-[12.5px]"
        data-move-dialog
      >
        <p class="m-0 text-[12px] text-muted">
          {{ tt('Your changes so far are pushed to a new branch (never to the main one), and the machine carries on from this conversation, even while this computer sleeps. The session here becomes read-only.') }}
        </p>
        <label class="flex flex-col gap-1 text-[12px] text-muted">
          {{ tt('Machine') }}
          <HSelect
            v-model="host"
            :aria-label="tt('Machine')"
            :options="options"
          />
        </label>
        <p
          v-if="error"
          role="alert"
          class="m-0 border border-bad px-3 py-2 text-[12px] text-bad"
        >
          {{ error }}
        </p>
        <p
          v-if="checking"
          class="m-0 flex items-center gap-2 text-[12px] text-muted"
        >
          <HLoader /> {{ tt('Looking at what would go') }}
        </p>
        <template v-else-if="check">
          <p
            v-if="facts"
            class="m-0 text-[12px] text-toned"
          >
            {{ facts }}
          </p>
          <p class="m-0 text-[12px] text-toned">
            {{ tt('There it runs in {0} mode', [modeLabel]) }}<template v-if="check.permissionNote">
              · {{ check.permissionNote }}
            </template>
          </p>
          <ul
            v-if="check.problems.length"
            role="alert"
            class="m-0 flex list-none flex-col gap-2 border border-bad p-3"
          >
            <li
              v-for="p in check.problems"
              :key="p.code"
              class="flex flex-col gap-0.5"
            >
              <span class="text-bad">{{ p.message }}</span>
              <span
                v-for="f in p.paths"
                :key="f"
                class="truncate font-mono text-[11.5px] text-muted"
              >{{ f }}</span>
            </li>
          </ul>
          <template v-else>
            <div
              v-if="check.working"
              class="flex items-center gap-2 text-[12px] text-muted"
            >
              <HToggle
                v-model="stopTurn"
                :label="tt('Stop the current turn first')"
              /> {{ tt('Stop the current turn first') }}
            </div>
            <label class="flex flex-col gap-1 text-[12px] text-muted">
              {{ tt('First message there (optional)') }}
              <textarea
                v-model="message"
                :aria-label="tt('First message there (optional)')"
                rows="3"
                class="resize-y border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
              />
            </label>
            <div class="flex items-center gap-2 text-[12px] text-muted">
              <HToggle
                v-model="openPullRequest"
                :label="tt('Open a draft pull request when it lands')"
              /> {{ tt('Open a draft pull request when it lands') }}
            </div>
          </template>
        </template>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <HBtn
          size="md"
          tone="muted"
          @click="open = false"
        >
          {{ tt('Cancel') }}
        </HBtn>
        <HBtn
          variant="primary"
          size="md"
          :disabled="!canMove"
          @click="move"
        >
          {{ moving ? tt('Moving') : tt('Move') }}
        </HBtn>
      </div>
    </template>
  </UModal>
</template>
