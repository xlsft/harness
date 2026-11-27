<script setup lang="ts">
// Best-of-N (docs/design/hosts.md, decisions 28 to 33). 'new': the form that starts N attempts of one prompt, in worktrees of
// this computer or on an always-on host. A group id: the attempts side by side, each with its state, its test result and its
// Changes pane; the user keeps one, and only then, after saying so, the others go with their worktrees and branches.
import type { AttemptGroupView, AttemptView, HostView } from '~/lib/hosts/types'
import { attemptCheckText, attemptStateText, repositoryKeyOf } from '~/lib/hosts/types'

const view = useAttemptsView()
const prefill = useState<{ prompt: string, cwd: string, hostId: string, repository: string, model: string }>('attempts-prefill', () => ({ prompt: '', cwd: '', hostId: '', repository: '', model: '' }))
const store = useSessionStore()
const toast = useToast()

const open = computed({
  get: () => !!view.value,
  set: (v) => {
    if (!v) view.value = ''
  }
})
const starting = computed(() => view.value === 'new')

function errorText(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}
const error = ref('')

// ---- starting a group ----
const prompt = ref('')
const count = ref(3)
const host = ref('')
const repository = ref('')
const testCommand = ref('')
const parallel = ref(0)
const budget = ref(0)
const hosts = ref<HostView[]>([])
const busy = ref(false)
const where = computed(() => [{ value: '', label: tt('Worktrees on this computer'), hint: prefill.value.cwd }, ...hosts.value.map(h => ({ value: h.id, label: h.name, hint: h.unavailableReason }))])

async function loadForm() {
  error.value = ''
  prompt.value = prefill.value.prompt
  host.value = prefill.value.hostId
  repository.value = prefill.value.repository
  try {
    hosts.value = await store.transport!.hosts().list()
  } catch {
    hosts.value = []
  }
  try {
    const p = await store.transport!.getUserPreferences()
    parallel.value = p.bestOfNParallel || 0
    budget.value = p.bestOfNBudgetUsd || 0
  } catch {
    // The server's caps apply either way.
  }
}

const canStart = computed(() => !!prompt.value.trim() && !busy.value && count.value >= 2 && count.value <= 5 && (host.value ? !!repositoryKeyOf(repository.value) : !!prefill.value.cwd))

async function startGroup() {
  if (!canStart.value) return
  busy.value = true
  error.value = ''
  try {
    const g = await store.transport!.hosts().startAttempts({
      hostId: host.value,
      count: count.value,
      prompt: prompt.value.trim(),
      title: '',
      repositoryKey: host.value ? repositoryKeyOf(repository.value) : '',
      baseRef: '',
      model: prefill.value.model,
      testCommand: testCommand.value.trim(),
      parallel: parallel.value,
      budgetUsd: budget.value,
      cwd: host.value ? '' : prefill.value.cwd,
      idempotencyKey: crypto.randomUUID()
    })
    view.value = g.id
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}

// ---- comparing ----
const group = ref<AttemptGroupView | null>(null)
const confirmDiscard = ref(false)
let timer: ReturnType<typeof setInterval> | undefined

async function loadGroup() {
  if (starting.value || !view.value) return
  try {
    group.value = await store.transport!.hosts().getGroup(view.value)
    // Sessions the group names that this window does not list yet (a host's, or just made).
    if (group.value.attempts.some(a => a.sessionId && !store.sessions[a.sessionId])) await store.refreshList()
  } catch (e) {
    error.value = errorText(e)
  }
}

watch(view, (v) => {
  clearInterval(timer)
  error.value = ''
  confirmDiscard.value = false
  group.value = null
  if (!v) return
  if (v === 'new') {
    void loadForm()
    return
  }
  void loadGroup()
  timer = setInterval(() => void loadGroup(), 3000)
}, { immediate: true })
onBeforeUnmount(() => clearInterval(timer))

const shown = computed(() => (group.value?.attempts ?? []).filter(a => a.state !== 'discarded'))
const going = computed(() => (group.value?.attempts ?? []).filter(a => !a.kept && a.state !== 'discarded'))

async function keep(a: AttemptView) {
  error.value = ''
  try {
    group.value = await store.transport!.hosts().keepAttempt(group.value!.id, a.index)
  } catch (e) {
    error.value = errorText(e)
  }
}

async function discard() {
  if (!confirmDiscard.value) {
    confirmDiscard.value = true
    return
  }
  error.value = ''
  try {
    group.value = await store.transport!.hosts().discardAttempts(group.value!.id)
    confirmDiscard.value = false
    toast.add({ title: tt('The other attempts are being removed') })
  } catch (e) {
    error.value = errorText(e)
  }
}

function openSession(id: string) {
  store.open(id)
  view.value = ''
}
</script>

<template>
  <UModal
    v-model:open="open"
    :title="starting ? tt('Best of N') : tt('Compare attempts')"
    :description="starting ? tt('One prompt, several attempts, each on a branch of its own; you keep one.') : group?.prompt"
    :ui="{ content: starting ? undefined : 'max-w-[min(1400px,96vw)] h-[90vh]' }"
  >
    <template #body>
      <p
        v-if="error"
        role="alert"
        class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
      >
        {{ error }}
      </p>
      <div
        v-if="starting"
        class="flex flex-col gap-3 text-[12.5px]"
        data-attempts-form
      >
        <label class="flex flex-col gap-1 text-[12px] text-muted">
          {{ tt('Prompt') }}
          <textarea
            v-model="prompt"
            :aria-label="tt('Prompt')"
            rows="4"
            class="resize-y border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
          />
        </label>
        <label class="flex flex-col gap-1 text-[12px] text-muted">
          {{ tt('Where') }}
          <HSelect
            v-model="host"
            :aria-label="tt('Where')"
            :options="where"
          />
        </label>
        <label
          v-if="host"
          class="flex flex-col gap-1 text-[12px] text-muted"
        >
          {{ tt('Repository') }}
          <input
            v-model="repository"
            :aria-label="tt('Repository')"
            placeholder="github.com/owner/name"
            class="border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
          >
        </label>
        <p
          v-else-if="!prefill.cwd"
          class="m-0 text-[12px] text-bad"
        >
          {{ tt('Choose a folder first: each attempt gets a worktree of its repository.') }}
        </p>
        <div class="flex flex-wrap gap-3">
          <label class="flex flex-col gap-1 text-[12px] text-muted">
            {{ tt('Attempts') }}
            <input
              v-model.number="count"
              type="number"
              min="2"
              max="5"
              :aria-label="tt('Attempts')"
              class="w-20 border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
            >
          </label>
          <label class="flex flex-col gap-1 text-[12px] text-muted">
            {{ tt('At once (0: all)') }}
            <input
              v-model.number="parallel"
              type="number"
              min="0"
              max="5"
              :aria-label="tt('At once (0: all)')"
              class="w-20 border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
            >
          </label>
          <label class="flex flex-col gap-1 text-[12px] text-muted">
            {{ tt('Budget per attempt, USD (0: none)') }}
            <input
              v-model.number="budget"
              type="number"
              min="0"
              step="0.5"
              :aria-label="tt('Budget per attempt, USD (0: none)')"
              class="w-28 border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
            >
          </label>
        </div>
        <label class="flex flex-col gap-1 text-[12px] text-muted">
          {{ tt('Test command (optional, runs in each checkout when a turn ends)') }}
          <input
            v-model="testCommand"
            :aria-label="tt('Test command')"
            placeholder="npm test"
            class="border border-line bg-transparent px-2 py-1 font-mono text-[12px] text-strong outline-none focus:border-accent"
          >
        </label>
        <p class="m-0 text-[11.5px] text-muted">
          {{ tt('Your organization may allow fewer attempts, fewer at once or a lower budget; the server applies its limits.') }}
        </p>
      </div>
      <div
        v-else-if="group"
        class="flex h-full min-h-0 gap-2 overflow-x-auto"
        data-attempts-compare
      >
        <section
          v-for="a in shown"
          :key="a.index"
          class="flex min-w-[320px] flex-1 flex-col border"
          :class="a.kept ? 'border-accent' : 'border-line'"
          :aria-label="tt('Attempt {0}', [a.index + 1])"
        >
          <header class="flex flex-col gap-1 border-b border-line px-3 py-2 text-[12px]">
            <div class="flex items-center gap-2">
              <span class="text-strong">{{ tt('Attempt {0}', [a.index + 1]) }}</span>
              <span
                v-if="a.kept"
                class="text-accent"
              >{{ tt('Kept') }}</span>
              <span class="ml-auto text-muted">{{ attemptStateText(a) }}</span>
            </div>
            <span
              v-if="a.branch"
              class="truncate font-mono text-[11.5px] text-muted"
            >{{ a.branch }}</span>
            <details
              v-if="a.check"
              class="text-[11.5px]"
            >
              <summary :class="a.check.exitCode === 0 && !a.check.refused ? 'text-ok' : 'text-bad'">
                {{ attemptCheckText(a.check) }}
              </summary>
              <pre class="m-0 max-h-40 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-toned">{{ a.check.output }}</pre>
            </details>
            <div class="flex gap-2">
              <HBtn
                v-if="a.sessionId"
                @click="openSession(a.sessionId)"
              >
                {{ tt('Open') }}
              </HBtn>
              <HBtn
                v-if="!a.kept && a.state !== 'discarding'"
                @click="keep(a)"
              >
                {{ tt('Keep this one') }}
              </HBtn>
            </div>
          </header>
          <div class="min-h-0 flex-1 overflow-auto">
            <DiffPane
              v-if="a.sessionId && store.sessions[a.sessionId]"
              :session="store.sessions[a.sessionId]!"
            />
            <p
              v-else
              class="m-0 px-3 py-2 text-[12px] text-muted"
            >
              {{ tt('No changes to show yet') }}
            </p>
          </div>
        </section>
      </div>
      <p
        v-else
        class="m-0 flex items-center gap-2 text-[12px] text-muted"
      >
        <HLoader /> {{ tt('Loading the attempts') }}
      </p>
    </template>
    <template #footer>
      <div class="flex w-full items-center justify-end gap-2">
        <template v-if="starting">
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
            :disabled="!canStart"
            @click="startGroup"
          >
            {{ busy ? tt('Starting') : tt('Start {0} attempts', [count]) }}
          </HBtn>
        </template>
        <template v-else-if="group">
          <span
            v-if="confirmDiscard"
            class="mr-auto truncate text-[12px] text-bad"
          >
            {{ tt('Removes {0} attempts, their checkouts and the branches {1}', [going.length, going.map(a => a.branch).filter(Boolean).join(', ')]) }}
          </span>
          <HBtn
            v-if="group.keptIndex >= 0 && going.length"
            size="md"
            :tone="confirmDiscard ? 'danger' : 'muted'"
            @click="discard"
          >
            {{ confirmDiscard ? tt('Discard them for good') : tt('Discard the others…') }}
          </HBtn>
          <HBtn
            size="md"
            tone="muted"
            @click="open = false"
          >
            {{ tt('Close') }}
          </HBtn>
        </template>
      </div>
    </template>
  </UModal>
</template>
