<script setup lang="ts">
// Inbox: every session and background agent of the user on every machine, by what it needs. The client folds and merges
// them (docs/design/coordinator.md); this lists the pages it returns, applies its live changes and sends what the user
// answers. The list is virtualised; the next page of a group loads as the list nears its end.
import { VList } from 'virtua/vue'
import { RespondToPermissionRequest_Decision, RespondToPlanRequest_Decision } from '~/gen/harness/agent/v1/agent_pb'
import {
  addPage,
  applyChange,
  ControlProjectRunRequest_Action,
  deviceText,
  emptyInbox,
  groupTitle,
  INBOX_GROUPS,
  inboxRows,
  InboxGroup,
  InboxRequest_Kind,
  itemLine,
  markLocal,
  nextToLoad,
  type InboxItem,
  type InboxRequest,
  type InboxRow,
  type InboxView
} from '~/lib/inbox/types'

const store = useSessionStore()
const api = computed(() => store.transport?.inbox())
const view = shallowRef<InboxView>(emptyInbox())
const only = ref<InboxGroup | undefined>()
const error = ref('')
const busy = ref('')
const loadingMore = ref(false)

const rows = computed<InboxRow[]>(() => inboxRows(view.value, only.value))

const message = (err: unknown) => (err instanceof Error ? err.message : String(err))
const touch = (v: InboxView) => (view.value = { ...v })

async function loadAll() {
  const a = api.value
  if (!a) return
  try {
    let v = emptyInbox()
    for (const g of INBOX_GROUPS) v = addPage(v, g, await a.list(g), true)
    touch(v)
    error.value = ''
  } catch (err) {
    error.value = message(err)
  }
}

async function loadMore() {
  const a = api.value
  const g = nextToLoad(view.value, only.value)
  if (!a || g === undefined || loadingMore.value) return
  loadingMore.value = true
  try {
    touch(addPage(view.value, g, await a.list(g, view.value.groups.get(g)!.next), false))
  } catch (err) {
    error.value = message(err)
  } finally {
    loadingMore.value = false
  }
}

const list = ref<InstanceType<typeof VList>>()
function onScroll(offset: number) {
  const l = list.value
  if (!l) return
  if (offset + l.viewportSize > l.scrollSize - 600) void loadMore()
}

let ctl: AbortController | undefined
async function follow() {
  const a = api.value
  if (!a) return
  ctl?.abort()
  ctl = new AbortController()
  const signal = ctl.signal
  while (!signal.aborted) {
    try {
      await a.watch({
        onChange: (c, counts) => touch(applyChange(view.value, c, counts)),
        onResync: () => void loadAll()
      }, signal)
    } catch {
      // The client went away for a moment: list again when the stream comes back.
    }
    if (signal.aborted) return
    await new Promise(r => setTimeout(r, 2000))
    await loadAll()
  }
}

onMounted(async () => {
  await loadAll()
  void follow()
})
onUnmounted(() => ctl?.abort())
watch(api, async () => {
  await loadAll()
  void follow()
})

async function act(key: string, fn: () => Promise<unknown>) {
  busy.value = key
  try {
    await fn()
    error.value = ''
  } catch (err) {
    error.value = message(err)
  } finally {
    busy.value = ''
  }
}

function markRead(it: InboxItem, read = true) {
  touch(markLocal(view.value, [it.sessionId], read))
  void act(`read:${it.sessionId}`, () => api.value!.markRead([it.sessionId], read))
}

function openSession(it: InboxItem) {
  if (it.unread) markRead(it)
  store.open(it.sessionId)
  navigateTo({ path: '/' })
}

function allow(it: InboxItem, r: InboxRequest, always = false) {
  const t = store.transport!
  const decision = always ? RespondToPermissionRequest_Decision.ALLOW_ALWAYS : RespondToPermissionRequest_Decision.ALLOW
  void act(`${it.sessionId}:${r.requestId}`, () => t.respondToPermission(it.sessionId, r.requestId, decision, always ? r.suggestedRules[0] : undefined))
}

function deny(it: InboxItem, r: InboxRequest) {
  void act(`${it.sessionId}:${r.requestId}`, () => store.transport!.respondToPermission(it.sessionId, r.requestId, RespondToPermissionRequest_Decision.DENY))
}

// A question with one single-choice part can be answered here; anything more opens the session.
const quickQuestion = (r: InboxRequest) => r.kind === InboxRequest_Kind.QUESTION && r.questions.length === 1 && !r.questions[0]!.multiSelect && r.questions[0]!.options.length > 0

function answer(it: InboxItem, r: InboxRequest, option: string) {
  void act(`${it.sessionId}:${r.requestId}`, () => store.transport!.answerQuestion(it.sessionId, r.requestId, { outcome: 'answered', answers: [{ selected: [option], text: '', skipped: false }] }))
}

function approvePlan(it: InboxItem, r: InboxRequest) {
  void act(`${it.sessionId}:${r.requestId}`, () => store.transport!.respondToPlan(it.sessionId, r.requestId, RespondToPlanRequest_Decision.APPROVE_MANUAL))
}

function resumeRun(it: InboxItem) {
  const run = it.projectRun
  if (!run) return
  void act(`run:${run.id}`, () => store.transport!.projectRuns().control(run.id, ControlProjectRunRequest_Action.RESUME, undefined, it.sessionId))
}

function stop(it: InboxItem) {
  void act(`stop:${it.sessionId}`, () => store.transport!.interrupt(it.sessionId))
}

const filters = computed(() => [
  { value: undefined, label: tt('All') },
  ...INBOX_GROUPS.map(g => ({ value: g, label: groupTitle(g), count: view.value.counts.get(g)?.total ?? 0 }))
])

const tone = (g: InboxGroup) => g === InboxGroup.NEEDS_INPUT ? 'border-l-accent' : g === InboxGroup.FAILED ? 'border-l-bad' : 'border-l-default'
</script>

<template>
  <section
    :aria-label="tt('Inbox')"
    class="flex h-full flex-col bg-default"
  >
    <div class="mx-auto flex w-full max-w-[920px] flex-1 flex-col gap-4 px-10 pt-[52px] pb-6 max-sm:px-4">
      <div class="flex items-center gap-3">
        <h1 class="m-0 flex-1 font-display text-[26px] font-bold text-strong">
          {{ tt('Inbox') }}
        </h1>
        <HBtn
          size="md"
          @click="loadAll"
        >
          {{ tt('Refresh') }}
        </HBtn>
      </div>
      <p class="m-0 text-[12.5px] text-muted">
        {{ tt('Sessions and agents on all your machines and always-on hosts, by what they need from you. Answer here, or open the session.') }}
      </p>
      <div
        role="tablist"
        :aria-label="tt('Groups')"
        class="flex flex-wrap gap-1.5"
      >
        <button
          v-for="f in filters"
          :key="String(f.value)"
          role="tab"
          :aria-selected="only === f.value"
          class="border px-2.5 py-[3px] text-[11.5px]"
          :class="only === f.value ? 'border-accent text-strong' : 'border-line text-muted hover:bg-hover'"
          @click="only = f.value"
        >
          {{ f.label }}<span
            v-if="'count' in f"
            class="ml-1 text-muted"
          >{{ f.count }}</span>
        </button>
      </div>
      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>
      <VList
        ref="list"
        v-slot="{ item: row }"
        :data="rows"
        class="min-h-0 flex-1"
        @scroll="onScroll"
      >
        <div
          v-if="row.kind === 'group'"
          :key="row.key"
          class="flex items-baseline gap-2 pt-4 pb-1.5 text-[12px] font-semibold text-strong"
        >
          {{ groupTitle(row.group) }}
          <span class="font-normal text-muted">{{ row.total }}</span>
          <span
            v-if="row.unread"
            class="font-normal text-accent"
          >{{ tt('{0} new', [row.unread]) }}</span>
        </div>
        <p
          v-else-if="row.kind === 'empty'"
          :key="row.key"
          class="m-0 py-1 text-[12px] text-muted"
        >
          {{ tt('Nothing here.') }}
        </p>
        <article
          v-else
          :key="row.key"
          :aria-label="row.item.title"
          class="mb-1.5 flex flex-col gap-1.5 border border-l-2 border-default px-3 py-2 text-[12.5px]"
          :class="tone(row.item.group)"
        >
          <div class="flex items-center gap-2">
            <span
              v-if="row.item.unread"
              class="size-1.5 shrink-0 bg-accent"
              :aria-label="tt('Unread')"
            />
            <button
              class="min-w-0 flex-1 truncate text-left font-medium text-strong hover:underline"
              @click="openSession(row.item)"
            >
              {{ row.item.title || row.item.sessionId }}
            </button>
            <span class="shrink-0 text-[11.5px] text-muted">{{ deviceText(row.item) }}</span>
          </div>
          <p class="m-0 line-clamp-2 text-toned">
            {{ itemLine(row.item) }}
          </p>
          <div
            v-if="row.item.agents.length"
            class="flex flex-wrap gap-1.5 text-[11px] text-muted"
          >
            <span
              v-for="a in row.item.agents.slice(-4)"
              :key="a.id"
              class="border border-line px-1.5"
            >{{ a.name }}</span>
          </div>
          <div class="flex flex-wrap items-center gap-1.5">
            <template
              v-for="r in row.item.requests.slice(0, 1)"
              :key="r.requestId"
            >
              <template v-if="r.kind === InboxRequest_Kind.PERMISSION && row.item.open">
                <HBtn
                  variant="primary"
                  size="sm"
                  :disabled="busy === `${row.item.sessionId}:${r.requestId}`"
                  @click="allow(row.item, r)"
                >
                  {{ tt('Allow') }}
                </HBtn>
                <HBtn
                  v-if="r.suggestedRules.length && !r.oneTimeOnly"
                  size="sm"
                  :disabled="busy === `${row.item.sessionId}:${r.requestId}`"
                  :title="r.suggestedRules[0]"
                  @click="allow(row.item, r, true)"
                >
                  {{ tt('Always allow') }}
                </HBtn>
                <HBtn
                  size="sm"
                  tone="danger"
                  :disabled="busy === `${row.item.sessionId}:${r.requestId}`"
                  @click="deny(row.item, r)"
                >
                  {{ tt('Deny') }}
                </HBtn>
              </template>
              <template v-else-if="quickQuestion(r) && row.item.open">
                <HBtn
                  v-for="o in r.questions[0]!.options"
                  :key="o"
                  size="sm"
                  :disabled="busy === `${row.item.sessionId}:${r.requestId}`"
                  @click="answer(row.item, r, o)"
                >
                  {{ o }}
                </HBtn>
              </template>
              <HBtn
                v-else-if="r.kind === InboxRequest_Kind.PLAN && row.item.open"
                variant="primary"
                size="sm"
                @click="approvePlan(row.item, r)"
              >
                {{ tt('Approve plan') }}
              </HBtn>
              <HBtn
                v-else-if="r.kind === InboxRequest_Kind.PROJECT_RUN"
                variant="primary"
                size="sm"
                @click="resumeRun(row.item)"
              >
                {{ tt('Resume run') }}
              </HBtn>
            </template>
            <span class="flex-1" />
            <HBtn
              size="sm"
              variant="ghost"
              @click="openSession(row.item)"
            >
              {{ tt('Open') }}
            </HBtn>
            <HBtn
              v-if="row.item.open && (row.item.working || row.item.requests.length)"
              size="sm"
              variant="ghost"
              tone="danger"
              @click="stop(row.item)"
            >
              {{ tt('Stop') }}
            </HBtn>
            <HBtn
              size="sm"
              variant="ghost"
              tone="muted"
              @click="markRead(row.item, !row.item.unread)"
            >
              {{ row.item.unread ? tt('Mark read') : tt('Mark unread') }}
            </HBtn>
          </div>
        </article>
      </VList>
    </div>
  </section>
</template>
