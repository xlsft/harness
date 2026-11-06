<script setup lang="ts">
// Session details (the right panel of Main.dc.html): environment, context, outputs, changes,
// agents and background tasks of the current session.
import type { SessionState, ToolItem } from '~/lib/session/types'
import { isDesktop, openExternal, pickFolder, revealInFolder } from '~/lib/desktop'
import { sessionWebUrl, type FolderInfo } from '~/lib/environment/types'
import type { RowMenuItem } from '~/components/RowMenu.vue'
import type { RecentFolder } from '~/gen/harness/agent/v1/agent_pb'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const now = useNow()
// The session relay switch (remote control): on mirrors the session to the harness server. A remote session's
// switch belongs to its machine (read-only here).
const relay = computed({
  get: () => props.session.meta.relayEnabled,
  set: async (on: boolean) => {
    try {
      await store.setSessionRelay(props.session.meta.id, on)
    } catch (e) {
      toast.add({ title: on ? tt('Could not turn on the session relay') : tt('Could not turn off the session relay'), description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
    }
  }
})
// The auto-fix switch: on, the client watches the session's pull request and wakes the session when CI fails, the branch
// conflicts or a comment arrives. The client decides everything; a refusal (no PR, no gh or glab) comes back as an error.
const autoFix = computed({
  get: () => props.session.meta.pullRequest?.monitoring ?? false,
  set: async (on: boolean) => {
    try {
      await store.setPrMonitor(props.session.meta.id, on)
    } catch (e) {
      toast.add({ title: on ? tt('Could not turn on auto-fix') : tt('Could not turn off auto-fix'), description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
    }
  }
})
const showAutoFix = computed(() => !props.session.meta.noFolder && !props.session.meta.remote && props.session.meta.engine === 'harness')
const pr = computed(() => props.session.meta.pullRequest)
const relayLocked = computed(() => props.session.meta.remote ? `Switched on ${props.session.meta.deviceName || 'its own machine'}` : props.session.meta.relayAvailable ? '' : props.session.meta.relayReason || 'Remote control is not available')

const tools = computed(() => props.session.items.filter((i): i is ToolItem => i.kind === 'tool'))
const outputs = computed(() => {
  const files = new Set<string>()
  for (const t of tools.value) {
    if ((t.name === 'Write' || t.name === 'Edit' || t.name === 'NotebookEdit') && editedPath(t)) files.add(basename(editedPath(t)))
  }
  return [...files].slice(-3)
})
const agents = computed(() => props.session.subagentOrder.map(id => props.session.subagents[id]!))
const runningAgents = computed(() => agents.value.filter(a => a.state === 'running' || a.state === 'queued'))
const tasks = computed(() => props.session.taskOrder.map(id => props.session.tasks[id]!))
const runningTasks = computed(() => tasks.value.filter(t => t.state === 'running'))
const repoName = computed(() => basename(props.session.meta.repo || props.session.meta.cwd))
const cwdParent = computed(() => props.session.meta.cwd.replace(/\/[^/]+$/, ''))
// Started without a folder: its private directory is shown as "No folder", without repository rows. Its
// environment is per-session on the client; editing it is not offered here.
const noFolder = computed(() => props.session.meta.noFolder)

const desktop = isDesktop()
const toast = useToast()
const { openTerminal } = useTerminalRequests()
const editing = ref(false)
const { openPane } = usePanes()
const browsing = ref<'' | 'change' | 'add'>('')

// Git facts of the folder (its web page for "Open repository…"), and the server's web address for the relay links.
const folder = ref<FolderInfo | null>(null)
const webUrl = ref('')
watch(() => [props.session.meta.id, props.session.meta.cwd, props.session.meta.gitBranch], async () => {
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
async function chosen(path: string | null, as?: 'change' | 'add') {
  const what = as ?? browsing.value
  browsing.value = ''
  if (!path || !store.transport) return
  const env = store.transport.environment()
  try {
    if (what === 'change') store.reopen(await env.setSessionFolder(props.session.meta.id, path))
    else {
      await env.addSessionDirectory(props.session.meta.id, path)
      toast.add({ title: tt('Folder added'), description: `${basename(path)} is a working directory of this session now.`, color: 'neutral' })
    }
  } catch (e) {
    toast.add({ title: what === 'change' ? tt('Could not change the folder') : tt('Could not add the folder'), description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
  }
}
// A session without a folder gets one from the same picker as the new-session screen.
const assigning = ref(false)
const recent = ref<RecentFolder[]>([])
watch(assigning, async (open) => {
  if (open && store.transport) recent.value = await store.transport.listRecentFolders().catch(() => [])
})
function assign(path: string) {
  assigning.value = false
  if (path) void chosen(path, 'change')
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
  if (noFolder.value) {
    return [
      ...(desktop && local ? [{ label: tt('Show in Finder'), run: () => revealInFolder(cwd) }] : []),
      { label: tt('Copy path'), run: () => copy(cwd, 'Path') },
      ...(local ? [null, { label: tt('Open in terminal'), run: () => openTerminal(cwd) }] : [])
    ]
  }
  return [
    ...(desktop && local ? [{ label: tt('Show in Finder'), run: () => revealInFolder(cwd) }] : []),
    ...(web ? [{ label: tt('Open repository on {0}', [repoHost.value]), run: () => openExternal(web) }] : []),
    null,
    { label: tt('Copy path'), run: () => copy(cwd, 'Path') },
    // A session on an ACP agent stays in the folder it started in: the client refuses both.
    ...(local && !props.session.meta.unsupported.includes('move_folder')
      ? [
          { label: tt('Change folder…'), run: () => pick('change'), disabled: props.session.meta.status === 'working' || props.session.meta.status === 'needs_input' },
          { label: tt('Add folder…'), run: () => pick('add') }
        ]
      : []),
    null,
    ...(local ? [{ label: tt('Open in terminal'), run: () => openTerminal(cwd) }] : [])
  ]
})
const relayMenu = computed<(RowMenuItem | null)[]>(() => relay.value
  ? [
      { label: tt('Open on the web'), run: () => openExternal(sessionLink.value) },
      { label: tt('Copy link'), run: () => copy(sessionLink.value, 'Link') }
    ]
  : [])
const branchMenu = computed<(RowMenuItem | null)[]>(() => [
  { label: tt('Copy branch name'), run: () => copy(props.session.meta.gitBranch || 'main', 'Branch name') },
  null,
  ...(props.session.meta.remote ? [] : [{ label: tt('Open in terminal'), run: () => openTerminal(props.session.meta.cwd) }])
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
    :aria-label="tt('Session details')"
    class="flex h-full min-w-0 flex-col overflow-x-hidden overflow-y-auto bg-default whitespace-nowrap"
  >
    <section class="flex flex-col border-b border-default px-[13px] py-3">
      <div class="flex items-center pb-1">
        <span class="flex-1 text-[12px] text-muted">{{ tt('Environment') }}</span>
        <HIconBtn
          v-if="!session.meta.remote && !noFolder"
          icon="settings"
          :label="tt('Edit environment')"
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
        <HIcon name="monitor" /><span class="min-w-0 truncate">{{ tt('remote · {0}', [session.meta.deviceName || 'another machine']) }}</span><span
          v-if="session.meta.deviceOnline !== null"
          class="shrink-0 text-[11px]"
          :class="session.meta.deviceOnline ? 'text-ok' : 'text-muted'"
        >· {{ session.meta.deviceOnline ? 'online' : 'offline' }}</span>
      </div>
      <div
        v-else
        class="nav-row"
      >
        <HIcon name="monitor" /><span class="min-w-0 truncate">{{ tt('local · {0}', [session.meta.deviceName || 'this machine']) }}</span>
      </div>
      <!-- No folder yet: pick one, as on the new-session screen (the session moves there) -->
      <div
        v-if="noFolder && session.meta.unsupported.includes('move_folder')"
        class="nav-row"
      >
        <HIcon name="chat" /><span class="min-w-0 flex-1 truncate">{{ tt('No folder') }}</span>
      </div>
      <UPopover
        v-else-if="noFolder && !session.meta.remote"
        v-model:open="assigning"
        :content="{ side: 'bottom', align: 'start', sideOffset: 4 }"
      >
        <button
          type="button"
          class="nav-row -mx-[9px] w-[calc(100%+18px)] px-[9px] text-left hover:bg-hover"
          :disabled="session.meta.status === 'working' || session.meta.status === 'needs_input'"
        >
          <HIcon name="chat" /><span class="min-w-0 flex-1 truncate">{{ tt('No folder') }}</span><span class="text-[11px] text-muted">{{ tt('Choose…') }}</span>
        </button>
        <template #content>
          <NewFolderMenu
            :folders="recent"
            cwd=""
            :branches="[]"
            current=""
            base-ref=""
            :worktree="false"
            @select="assign"
            @browse="assigning = false; pick('change')"
          />
        </template>
      </UPopover>
      <RowMenu
        v-else
        :items="folderMenu"
        :label="tt('Folder')"
      >
        <button
          type="button"
          class="nav-row -mx-[9px] w-[calc(100%+18px)] px-[9px] text-left hover:bg-hover"
        >
          <template v-if="noFolder">
            <HIcon name="chat" /><span class="min-w-0 truncate">{{ tt('No folder') }}</span>
          </template>
          <template v-else>
            <HIcon name="folder" /><span class="shrink-0">{{ repoName }}</span><span class="min-w-0 truncate text-[11px] text-muted">· {{ cwdParent }}</span>
          </template>
        </button>
      </RowMenu>
      <RowMenu
        v-if="!noFolder && (!folder || folder.repositoryPath)"
        :items="branchMenu"
        :label="tt('Branch')"
      >
        <button
          type="button"
          class="nav-row -mx-[9px] w-[calc(100%+18px)] px-[9px] text-left hover:bg-hover"
        >
          <HIcon name="branch" /><span class="min-w-0 truncate">{{ session.meta.gitBranch || 'main' }}</span>
        </button>
      </RowMenu>
      <div
        v-if="session.meta.worktree"
        class="nav-row"
        :title="session.meta.worktree.path"
      >
        <HIcon name="diff" /><span class="min-w-0 truncate">{{ tt('Worktree') }}</span><span class="min-w-0 truncate text-[11px] text-muted">{{ tt('from {0}', [session.meta.worktree.baseRef]) }}</span>
      </div>
      <div class="nav-row">
        <HIcon name="relay" /><span class="min-w-0 truncate">{{ tt('Session relay') }}</span><span class="flex-1" /><RowMenu
          v-if="sessionLink && relayMenu.length"
          :items="relayMenu"
          :label="tt('Session relay')"
          align="end"
        >
          <HIconBtn
            icon="more"
            :label="tt('Session relay actions')"
            :size="26"
            :icon-size="13"
          />
        </RowMenu><UTooltip
          :text="relayLocked"
          :disabled="!relayLocked"
          :delay-duration="300"
          :content="{ side: 'bottom', sideOffset: 4 }"
        >
          <span class="flex"><HToggle
            v-model="relay"
            :label="tt('Session relay')"
            :disabled="!!relayLocked"
          /></span>
        </UTooltip>
      </div>
      <div
        v-if="showAutoFix"
        class="nav-row"
        :title="tt('Watch this session\'s pull request: a CI failure, a conflict or a new comment wakes the session')"
      >
        <HIcon name="pull-request" /><span class="min-w-0 truncate">{{ tt('Auto-fix') }}</span><span class="flex-1" /><span class="flex"><HToggle
          v-model="autoFix"
          :label="tt('Auto-fix')"
        /></span>
      </div>
    </section>

    <DetailsContext :session="session" />

    <section
      v-if="outputs.length"
      class="flex flex-col border-b border-default px-[13px] py-3"
    >
      <span class="pb-1 text-[12px] text-muted">{{ tt('Outputs') }}</span>
      <a
        v-for="f in outputs"
        :key="f"
        href="#"
        class="nav-row hover:bg-hover"
      ><HIcon name="file" />{{ f }}</a>
    </section>

    <section class="flex flex-col gap-[7px] border-b border-default px-[13px] py-3">
      <span class="text-[12px] text-muted">{{ tt('Changes') }}</span>
      <div class="flex items-center gap-[9px] text-[12.5px]">
        <button
          type="button"
          :title="tt('Open changes')"
          class="-mx-[9px] flex items-center gap-[9px] px-[9px] py-1 hover:bg-hover"
          @click="openPane('diff')"
        >
          <HIcon name="pull-request" />
          <span class="text-ok">+{{ formatCount(session.meta.additions) }}</span><span class="text-bad">−{{ formatCount(session.meta.deletions) }}</span>
        </button>
        <span class="flex-1" />
        <button
          v-if="pr"
          type="button"
          class="min-w-0 shrink-0 truncate border border-default px-2.5 py-1 text-[11.5px] text-strong hover:bg-hover"
          :title="prDetails(pr)"
          @click="openExternal(pr.url)"
        >
          {{ tt('PR {0}', [pr.number]) }} <span :class="prChip(pr).cls">{{ prChip(pr).text }}</span>
        </button>
        <button
          v-else-if="!noFolder"
          type="button"
          class="shrink-0 border border-default px-2.5 py-1 text-[11.5px] text-strong hover:bg-hover"
        >
          {{ tt('Create PR') }}
        </button>
      </div>
    </section>

    <section
      v-if="!session.meta.unsupported.includes('sub_sessions')"
      class="flex flex-col border-b border-default px-[13px] py-3"
    >
      <div class="flex items-center pb-1">
        <span class="truncate text-[12px] text-muted">{{ tt('Agents') }}</span><span class="flex-1" /><span class="shrink-0 pl-2 text-[10.5px] text-muted">{{ tt('{0} running', [runningAgents.length]) }}</span>
      </div>
      <div
        v-for="a in runningAgents"
        :key="a.id"
        class="nav-row"
      >
        <HIcon
          name="agent"
          class="text-accent"
        /><span class="truncate">{{ a.name }}</span>
        <span class="flex-1" /><span class="text-[10.5px] text-muted">{{ age(now - a.startedAt) }}</span>
      </div>
      <div class="nav-row text-muted">
        <HIcon name="check" />{{ tt('{0} finished', [agents.length - runningAgents.length]) }}
      </div>
    </section>

    <section
      v-if="!session.meta.unsupported.includes('background_tasks')"
      class="flex flex-col px-[13px] py-3"
    >
      <div class="flex items-center pb-1">
        <span class="truncate text-[12px] text-muted">{{ tt('Background tasks') }}</span><span class="flex-1" /><span class="shrink-0 pl-2 text-[10.5px] text-muted">{{ tt('{0} running', [runningTasks.length]) }}</span>
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
        <HIcon name="check" />{{ tt('{0} finished', [tasks.length - runningTasks.length]) }}
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
