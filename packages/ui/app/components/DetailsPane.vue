<script setup lang="ts">
// Session details (the right panel of Main.dc.html): environment, context, outputs, changes,
// agents and background tasks of the current session.
import type { SessionState, ToolItem } from '~/lib/session/types'
import { isSubagent } from '~/lib/session/reducer'
import { isDesktop, openExternal, pickFolder, revealInFolder } from '~/lib/desktop'
import { sessionWebUrl, type FolderInfo } from '~/lib/environment/types'
import type { RowMenuItem } from '~/components/RowMenu.vue'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const now = useNow()
const relay = ref(true)

const model = computed(() => store.models.find(m => m.id === props.session.meta.model))

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
const editing = ref(false)
const { openPane } = usePanes()
const browsing = ref<'' | 'change' | 'add'>('')

// Git facts of the folder (its web page for "Open repository…"), and the server's web address for the relay links.
const folder = ref<FolderInfo | null>(null)
const webUrl = ref('')
watch(() => [props.session.meta.id, props.session.meta.cwd], async () => {
  folder.value = null
  if (!store.transport || props.session.meta.remote) return
  try {
    folder.value = await store.transport.environment().folderInfo(props.session.meta.cwd)
  } catch { /* no git facts: the menu leaves the repository item out */ }
}, { immediate: true })
onMounted(async () => {
  try {
    webUrl.value = (await store.transport?.initialize())?.webUrl ?? ''
  } catch { /* no web link */ }
})
const sessionLink = computed(() => sessionWebUrl(webUrl.value, props.session.meta.id))
const repoHost = computed(() => {
  try {
    const h = new URL(folder.value?.webUrl ?? '').host
    return h === 'github.com' ? 'GitHub' : h
  } catch {
    return ''
  }
})

// "Change folder…" moves the idle session to another folder (the client reopens it there); "Add folder…" adds a
// working directory to it and its environment.
async function chosen(path: string | null) {
  const what = browsing.value
  browsing.value = ''
  if (!path || !store.transport) return
  const env = store.transport.environment()
  try {
    if (what === 'change') store.reopen(await env.setSessionFolder(props.session.meta.id, path))
    else {
      await env.addSessionDirectory(props.session.meta.id, path)
      toast.add({ title: 'Folder added', description: `${basename(path)} is a working directory of this session now.`, color: 'neutral' })
    }
  } catch (e) {
    toast.add({ title: what === 'change' ? 'Could not change the folder' : 'Could not add the folder', description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
  }
}
async function pick(what: 'change' | 'add') {
  if (!desktop) {
    browsing.value = what
    return
  }
  browsing.value = what
  await chosen(await pickFolder(props.session.meta.cwd))
}
async function copy(text: string, what: string) {
  await navigator.clipboard.writeText(text)
  toast.add({ title: `${what} copied`, color: 'neutral' })
}
// Menus of the folder and branch rows (a remote session's folders are on another machine: copy only).
const folderMenu = computed<(RowMenuItem | null)[]>(() => {
  const cwd = props.session.meta.cwd
  const local = !props.session.meta.remote
  const web = folder.value?.webUrl
  return [
    ...(desktop && local ? [{ label: 'Show in Finder', run: () => revealInFolder(cwd) }] : []),
    ...(web ? [{ label: `Open repository on ${repoHost.value}`, run: () => openExternal(web) }] : []),
    null,
    { label: 'Copy path', run: () => copy(cwd, 'Path') },
    ...(local
      ? [
          { label: 'Change folder…', run: () => pick('change'), disabled: props.session.meta.status === 'working' || props.session.meta.status === 'needs_input' },
          { label: 'Add folder…', run: () => pick('add') }
        ]
      : []),
    null,
    ...(local ? [{ label: 'Open in terminal', run: () => openTerminal(cwd) }] : [])
  ]
})
const relayMenu = computed<(RowMenuItem | null)[]>(() => [
  { label: 'Open on the web', run: () => openExternal(sessionLink.value) },
  { label: 'Copy link', run: () => copy(sessionLink.value, 'Link') }
])
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
      <div class="flex items-center pb-1">
        <span class="flex-1 text-[12px] text-muted">Environment</span>
        <HIconBtn
          v-if="!session.meta.remote"
          icon="settings"
          label="Edit environment"
          :size="26"
          :icon-size="13"
          class="-my-1"
          @click="editing = true"
        />
      </div>
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
        <HIcon name="relay" /><span class="min-w-0 truncate">Session relay</span><span class="flex-1" /><RowMenu
          v-if="sessionLink"
          :items="relayMenu"
          label="Session relay"
          align="end"
        >
          <HIconBtn
            icon="more"
            label="Session relay actions"
            :size="26"
            :icon-size="13"
          />
        </RowMenu><HToggle
          v-model="relay"
          label="Session relay"
        />
      </div>
    </section>

    <DetailsContext :session="session" />

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
        <button
          type="button"
          title="Open changes"
          class="-mx-[9px] flex items-center gap-[9px] px-[9px] py-1 hover:bg-hover"
          @click="openPane('diff')"
        >
          <HIcon name="pull-request" />
          <span class="text-ok">+{{ formatCount(session.meta.additions) }}</span><span class="text-bad">−{{ formatCount(session.meta.deletions) }}</span>
        </button>
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
    <EnvironmentModal
      v-model:open="editing"
      :session="session"
    />
    <UModal
      :open="!!browsing && !desktop"
      :ui="{ content: 'w-auto max-w-none bg-transparent ring-0' }"
      @update:open="v => !v && (browsing = '')"
    >
      <template #content>
        <NewDirectoryBrowser
          :start="session.meta.cwd"
          @pick="chosen"
          @close="browsing = ''"
        />
      </template>
    </UModal>
  </aside>
</template>
