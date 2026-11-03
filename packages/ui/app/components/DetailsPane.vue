<script setup lang="ts">
// Session details (the right panel of Main.dc.html): environment, context, outputs, changes,
// agents and background tasks of the current session.
import type { SessionState, ToolItem } from '~/lib/session/types'
import { isSubagent } from '~/lib/session/reducer'
import { isDesktop, revealInFolder } from '~/lib/desktop'
import type { RowMenuItem } from '~/components/RowMenu.vue'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const now = useNow()
const relay = ref(true)

const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
const contextWindow = computed(() => model.value?.contextWindow || 200_000)
const contextPct = computed(() => Math.min(100, Math.round((props.session.contextTokens / contextWindow.value) * 100)))

const tools = computed(() => props.session.items.filter((i): i is ToolItem => i.kind === 'tool'))
const outputs = computed(() => {
  const files = new Set<string>()
  for (const t of tools.value) {
    if ((t.name === 'Write' || t.name === 'Edit') && typeof t.input.file_path === 'string') files.add(basename(t.input.file_path))
  }
  return [...files].slice(-3)
})
const agents = computed(() => tools.value.filter(isSubagent))
const runningAgents = computed(() => agents.value.filter(a => a.status === 'running'))
const tasks = computed(() => props.session.taskOrder.map(id => props.session.tasks[id]!))
const runningTasks = computed(() => tasks.value.filter(t => t.state === 'running'))
const repoName = computed(() => basename(props.session.meta.repo || props.session.meta.cwd))
const cwdParent = computed(() => props.session.meta.cwd.replace(/\/[^/]+$/, ''))

const desktop = isDesktop()
const toast = useToast()
const { openTerminal } = useTerminalRequests()
async function copy(text: string, what: string) {
  await navigator.clipboard.writeText(text)
  toast.add({ title: `${what} copied`, color: 'neutral' })
}
// Menus of the folder and branch rows (a remote session's folders are on another machine: copy only).
const folderMenu = computed<(RowMenuItem | null)[]>(() => {
  const cwd = props.session.meta.cwd
  const local = !props.session.meta.remote
  return [
    ...(desktop && local ? [{ label: 'Show in Finder', run: () => revealInFolder(cwd) }] : []),
    null,
    { label: 'Copy path', run: () => copy(cwd, 'Path') },
    null,
    ...(local ? [{ label: 'Open in terminal', run: () => openTerminal(cwd) }] : [])
  ]
})
const branchMenu = computed<(RowMenuItem | null)[]>(() => [
  { label: 'Copy branch name', run: () => copy(props.session.meta.gitBranch || 'main', 'Branch name') },
  null,
  ...(props.session.meta.remote ? [] : [{ label: 'Open in terminal', run: () => openTerminal(props.session.meta.cwd) }])
])

function age(ms: number) {
  return `${Math.max(1, Math.round(ms / 60_000))}m`
}

function taskMeta(command: string, tail: string) {
  const port = tail.match(/localhost:(\d+)/)?.[1]
  return port ? { text: `:${port}`, cls: 'text-ok' } : null
}
</script>

<template>
  <aside
    aria-label="Session details"
    class="flex h-full min-w-0 flex-col overflow-x-hidden overflow-y-auto bg-default whitespace-nowrap"
  >
    <section class="flex flex-col border-b border-default px-[13px] py-3">
      <span class="pb-1 text-[12px] text-muted">Environment</span>
      <div
        v-if="session.meta.remote"
        class="nav-row"
      >
        <HIcon name="monitor" /><span class="min-w-0 truncate">remote · {{ session.meta.deviceName || 'another machine' }}</span><span
          v-if="session.meta.deviceOnline !== null"
          class="shrink-0 text-[11px]"
          :class="session.meta.deviceOnline ? 'text-ok' : 'text-muted'"
        >· {{ session.meta.deviceOnline ? 'online' : 'offline' }}</span>
      </div>
      <div
        v-else
        class="nav-row"
      >
        <HIcon name="monitor" /><span class="min-w-0 truncate">local · {{ session.meta.deviceName || 'this machine' }}</span>
      </div>
      <RowMenu
        :items="folderMenu"
        label="Folder"
      >
        <button
          type="button"
          class="nav-row -mx-[9px] w-[calc(100%+18px)] px-[9px] text-left hover:bg-hover"
        >
          <HIcon name="folder" /><span class="shrink-0">{{ repoName }}</span><span class="min-w-0 truncate text-[11px] text-muted">· {{ cwdParent }}</span>
        </button>
      </RowMenu>
      <RowMenu
        :items="branchMenu"
        label="Branch"
      >
        <button
          type="button"
          class="nav-row -mx-[9px] w-[calc(100%+18px)] px-[9px] text-left hover:bg-hover"
        >
          <HIcon name="branch" /><span class="min-w-0 truncate">{{ session.meta.gitBranch || 'main' }}</span>
        </button>
      </RowMenu>
      <div
        v-if="!session.meta.remote"
        class="nav-row"
      >
        <HIcon name="relay" /><span class="min-w-0 truncate">Session relay</span><span class="flex-1" /><HToggle
          v-model="relay"
          label="Session relay"
        />
      </div>
    </section>

    <section class="flex flex-col gap-[7px] border-b border-default px-[13px] py-3">
      <div class="flex items-center">
        <span class="text-[12px] text-muted">Context</span><span class="flex-1" /><span class="text-[12px] text-strong">{{ contextPct }}%</span>
      </div>
      <div class="flex h-1 bg-raised">
        <span
          class="bg-accent"
          :style="{ width: `${contextPct}%` }"
        />
      </div>
      <div class="flex justify-between text-[10.5px] text-muted">
        <span>{{ formatTokens(session.contextTokens) }} / {{ formatTokens(contextWindow) }} tok</span><span>{{ formatCost(session.costUsd) }}</span>
      </div>
    </section>

    <section
      v-if="outputs.length"
      class="flex flex-col border-b border-default px-[13px] py-3"
    >
      <span class="pb-1 text-[12px] text-muted">Outputs</span>
      <a
        v-for="f in outputs"
        :key="f"
        href="#"
        class="nav-row hover:bg-hover"
      ><HIcon name="file" />{{ f }}</a>
    </section>

    <section class="flex flex-col gap-[7px] border-b border-default px-[13px] py-3">
      <span class="text-[12px] text-muted">Changes</span>
      <div class="flex items-center gap-[9px] text-[12.5px]">
        <HIcon name="pull-request" />
        <span class="text-ok">+{{ formatCount(session.meta.additions) }}</span><span class="text-bad">−{{ formatCount(session.meta.deletions) }}</span>
        <span class="flex-1" />
        <button
          type="button"
          class="shrink-0 border border-default px-2.5 py-1 text-[11.5px] text-strong hover:bg-hover"
        >
          {{ session.meta.pullRequest ? `PR ${session.meta.pullRequest.number}` : 'Create PR' }}
        </button>
      </div>
    </section>

    <section class="flex flex-col border-b border-default px-[13px] py-3">
      <div class="flex items-center pb-1">
        <span class="truncate text-[12px] text-muted">Agents</span><span class="flex-1" /><span class="shrink-0 pl-2 text-[10.5px] text-muted">{{ runningAgents.length }} running</span>
      </div>
      <div
        v-for="a in runningAgents"
        :key="a.id"
        class="nav-row"
      >
        <HIcon
          name="agent"
          class="text-accent"
        /><span class="truncate">{{ String(a.input.description ?? a.summary) }}</span>
        <span class="flex-1" /><span class="text-[10.5px] text-muted">{{ age(now - a.startedAt) }}</span>
      </div>
      <div class="nav-row text-muted">
        <HIcon name="check" />{{ agents.length - runningAgents.length }} finished
      </div>
    </section>

    <section class="flex flex-col px-[13px] py-3">
      <div class="flex items-center pb-1">
        <span class="truncate text-[12px] text-muted">Background tasks</span><span class="flex-1" /><span class="shrink-0 pl-2 text-[10.5px] text-muted">{{ runningTasks.length }} running</span>
      </div>
      <div
        v-for="t in runningTasks"
        :key="t.id"
        class="nav-row"
      >
        <HIcon name="term" /><span class="truncate">{{ t.command }}</span>
        <span class="flex-1" />
        <span
          v-if="taskMeta(t.command, t.tail)"
          class="text-[10.5px]"
          :class="taskMeta(t.command, t.tail)!.cls"
        >{{ taskMeta(t.command, t.tail)!.text }}</span>
        <span
          v-else
          class="text-[10.5px] text-muted"
        >{{ formatDuration(now - t.startedAt) }}</span>
      </div>
      <div class="nav-row text-muted">
        <HIcon name="check" />{{ tasks.length - runningTasks.length }} finished
      </div>
    </section>
  </aside>
</template>
