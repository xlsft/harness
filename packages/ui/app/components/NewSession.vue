<script setup lang="ts">
// New session screen (like Claude Code's): pick where the session runs, then type the first message.
// Centre: No folder, recent folders, Choose folder…, environment, the worktree switch. Bottom: the composer with
// chips for environment, folder (No folder / recent / Open folder… / branch), worktree, model, effort and mode.
// The session is created on the client with the first message. Shown for ?new=1 (sidebar New session, ⌘N) and
// on a client with no sessions. It always starts with no folder (a chat: the client gives the session a private
// directory); ?cwd= (a sidebar group's "+") preselects a folder. Worktree and the folder's environment need a
// folder, so they are hidden without one. Model, effort and mode are remembered; the folder is not.
// Picking an ACP agent's model starts the session on that agent: the mode chip then lists the agent's modes, and what
// the agent cannot do (effort, worktree) is hidden.
// With ?fork=<id> ("Continue here" on a session of another machine) the new session starts from a copy of that
// session's transcript, in the folder picked here.
// The text box is the session's (ComposerBox), less what needs a session: no shell or memory, the commands known
// without one (none for an agent: it advertises them once started), and @ lists the folders of the chosen folder, for
// ListDirectory shows folders and SuggestFiles needs a session; with no folder chosen @ offers the folder picker.
import type { Branch, RecentFolder } from '~/gen/harness/agent/v1/agent_pb'
import type { EffortName, PermissionModeName } from '~/lib/session/types'
import { SessionFeature } from '~/gen/harness/agent/v1/acp_pb'
import { fitAcpChoice, newSessionAgentEffort, newSessionChoice, type ModelScope } from '~/lib/acp/modelChoices'
import { readAttachments } from '~/lib/session/attachments'
import type { ComposerFile, MenuOption } from '~/utils/composer'
import { isDesktop, pickFolder } from '~/lib/desktop'

const store = useSessionStore()
const collapsed = useState('sidebar-collapsed', () => false)
const route = useRoute()
const router = useRouter()

const PREFS = 'harness-new-session'
const RECENT = 'harness-recent-cwds'

const folders = ref<RecentFolder[]>([])
const cwd = ref('')
const worktree = ref(false)
const baseRef = ref('')
const branches = ref<Branch[]>([])
const current = ref('')
const model = ref('')
// The engine of the chosen model when it is an ACP agent's ("acp:<id>"), and that agent's mode and effort to start on.
const engine = ref('')
const acpMode = ref('')
const acpEffort = ref('')
const effort = ref<EffortName>('medium')
// The level last chosen by hand on any model: what a model without its own or a recommended level starts on. A level
// that only followed a model's recommendation does not get here.
const generalEffort = ref<EffortName>('medium')
const mode = ref<PermissionModeName>('auto')
const text = ref('')
const files = ref<ComposerFile[]>([])
const busy = ref(false)
const error = ref('')
// One open menu at a time; the environment menu has two triggers (centre card and footer chip).
const menu = ref<'' | 'env' | 'env-centre' | 'folder' | 'model' | 'effort' | 'mode'>('')
const browsing = ref(false)
const box = ref<{ focusEnd: () => void } | null>(null)
const toast = useToast()

const name = (p: string) => p.replace(/\/+$/, '').split('/').pop() || p

const forkOf = computed(() => typeof route.query.fork === 'string' ? store.sessions[route.query.fork] : undefined)

function readJSON<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    return raw ? JSON.parse(raw) as T : fallback
  } catch {
    return fallback
  }
}
function writeJSON(key: string, v: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(v))
  } catch {
    // Not remembered; harmless.
  }
}

// Recent folders come from the client; a client without ListRecentFolders falls back to this browser's
// history and the folders of the listed sessions.
async function loadFolders() {
  try {
    folders.value = await store.transport!.listRecentFolders()
  } catch {
    const paths = [...readJSON<string[]>(RECENT, []), ...store.sessionList.map(s => s.meta.cwd)]
    folders.value = [...new Set(paths.filter(Boolean))].map(p => ({ path: p, name: name(p), exists: true }) as RecentFolder)
  }
}

async function loadBranches() {
  branches.value = []
  current.value = ''
  if (!cwd.value) return
  try {
    const res = await store.transport!.listBranches(cwd.value)
    branches.value = res.branches
    current.value = res.current
  } catch {
    // Older client: no branch list; the worktree starts from HEAD.
  }
}

onMounted(async () => {
  const prefs = readJSON<{ worktree?: boolean, model?: string, engine?: string, acpMode?: string, acpEffort?: string, effort?: EffortName, mode?: PermissionModeName }>(PREFS, {})
  worktree.value = prefs.worktree ?? false
  model.value = prefs.model ?? ''
  engine.value = prefs.engine ?? ''
  acpMode.value = prefs.acpMode ?? ''
  acpEffort.value = prefs.acpEffort ?? ''
  fitChoice()
  generalEffort.value = prefs.effort ?? 'medium'
  fitStartEffort()
  mode.value = fitMode(prefs.mode ?? 'auto', store.disabledModes)
  await loadFolders()
  const asked = newSessionFolder(route.query)
  cwd.value = asked
  if (asked && !folders.value.some(f => f.path === asked)) folders.value.unshift({ path: asked, name: name(asked), exists: true } as RecentFolder)
  box.value?.focusEnd()
})

// The server's policy can disable the chosen mode while this screen is open.
watch(() => store.disabledModes, (disabled) => {
  mode.value = fitMode(mode.value, disabled)
})

// "+" next to a sidebar group, or the top New session (no cwd: back to no folder), while this screen is open.
watch(() => route.query.cwd, (asked) => {
  if (route.query.new) choose(newSessionFolder({ cwd: asked }))
})

watch(cwd, () => {
  baseRef.value = ''
  loadBranches()
})

// "Open folder…": the native picker in the desktop app, the client's directory browser on the web.
async function browse() {
  menu.value = ''
  if (!isDesktop()) {
    browsing.value = true
    return
  }
  const path = await pickFolder(cwd.value)
  if (path) choose(path)
}

// '' is No folder.
function choose(path: string) {
  cwd.value = path
  menu.value = ''
  browsing.value = false
  box.value?.focusEnd()
  if (path && !folders.value.some(f => f.path === path)) folders.value.unshift({ path, name: name(path), exists: true } as RecentFolder)
}

// What the client finds for the chosen folder. An agent's own commands are known once its session runs.
const commands = useSlashMenu(() => engine.value ? null : { cwd: cwd.value }, () => text.value)
const suggest = computed(() => {
  const folder = cwd.value
  return async (q: string): Promise<MenuOption[]> => {
    if (!folder) return [{ key: 'choose-folder', label: tt('Choose folder…'), detail: 'to mention what is in it', icon: 'folder', iconCls: 'text-muted', run: browse }]
    const { dir, sub, match } = folderLookup(folder, q)
    try {
      const { entries } = await store.transport!.listDirectory(dir)
      return fileMentionOptions(entries.filter(e => e.name.toLowerCase().includes(match)).slice(0, 8).map(e => `${sub}${e.name}/`))
    } catch {
      return []
    }
  }
})

// The chosen agent; a session continuing another machine's transcript cannot run on one.
const agent = computed(() => engine.value ? store.acpAgents.find(a => a.engine === engine.value) : undefined)
const scope = computed<ModelScope>(() => ({ kind: forkOf.value ? 'server' : 'new' }))
const noWorktree = computed(() => !!agent.value?.unsupportedFeatures.includes(SessionFeature.WORKTREE))

// A remembered agent may be gone, signed out or without that model: back to what still can run. Not before the agents
// are known.
function fitChoice() {
  if (forkOf.value && engine.value) {
    engine.value = ''
    model.value = ''
  }
  if (!store.acpLoaded || !engine.value) return
  const fit = fitAcpChoice({ engine: engine.value, model: model.value }, store.acpAgents)
  engine.value = fit.engine
  model.value = fit.model
}
watch(() => [store.acpLoaded, store.acpAgents, forkOf.value], fitChoice)
watch(agent, (a) => {
  if (a && !a.modes.some(m => m.id === acpMode.value)) acpMode.value = ''
  if (a && !a.efforts.some(e => e.id === acpEffort.value)) acpEffort.value = ''
})
const acpModes = computed(() => agent.value ? { modes: agent.value.modes, current: acpMode.value || agent.value.currentModeId } : undefined)

function pickModel(id: string, from: string) {
  model.value = id
  engine.value = from
  acpMode.value = ''
  acpEffort.value = ''
}

const modelLabel = computed(() => {
  if (engine.value) return agent.value?.models.find(m => m.id === model.value)?.name || model.value || agent.value?.name || 'Agent'
  const m = store.models.find(x => x.id === model.value) ?? (model.value ? undefined : store.models[0])
  return m?.displayName || model.value || m?.id || 'default model'
})
const modelId = computed(() => model.value || store.models[0]?.id || '')
const serverModel = computed(() => store.models.find(m => m.id === modelId.value))
const efforts = computed(() => engine.value ? [] : modelEfforts(serverModel.value))
const recommended = computed(() => engine.value ? undefined : serverModel.value?.defaultEffort)
// The effort chip: the agent's own levels on an agent, from the cached catalog.
const effortChoice = computed(() => engine.value
  ? newSessionAgentEffort(agent.value, acpEffort.value)
  : { levels: harnessEffortLevels(efforts.value), current: efforts.value.length ? effort.value : undefined, recommended: recommended.value })
function pickEffort(level: string) {
  if (engine.value) {
    acpEffort.value = level
    return
  }
  effort.value = generalEffort.value = level as EffortName
  // The user's own for this model from now on; a level nobody picked stays unremembered, so it follows the recommendation.
  const prefs = readJSON<{ effortByModel?: Record<string, EffortName> }>(PREFS, {})
  writeJSON(PREFS, { ...prefs, effortByModel: { ...prefs.effortByModel, [modelId.value]: level as EffortName } })
}
// Each model starts at its own remembered effort (here or in Settings → Models), else its recommended one, else the last
// chosen or the nearest level it offers.
function fitStartEffort() {
  const own = readJSON<{ effortByModel?: Record<string, EffortName> }>(PREFS, {}).effortByModel?.[modelId.value]
  effort.value = startEffort({ own, recommended: recommended.value, fallback: generalEffort.value, levels: efforts.value }) ?? effort.value
}
watch([model, efforts, recommended], fitStartEffort)

async function start() {
  const msg = text.value.trim()
  if ((!msg && !files.value.length) || busy.value) return
  busy.value = true
  error.value = ''
  try {
    // Before the session exists: a file that cannot be sent leaves no empty session behind.
    const attachments = await readAttachments(files.value)
    const id = await store.createSession({
      cwd: cwd.value,
      model: model.value,
      forkOf: forkOf.value?.meta.id,
      ...newSessionChoice({
        engine: engine.value,
        agent: agent.value,
        acpMode: acpMode.value,
        acpEffort: acpEffort.value,
        effort: efforts.value.length ? effort.value : undefined,
        mode: mode.value,
        worktree: cwd.value && worktree.value ? { baseRef: baseRef.value } : undefined
      })
    })
    const { cwd: _old, ...prefs } = readJSON<Record<string, unknown>>(PREFS, {})
    writeJSON(PREFS, { ...prefs, model: model.value, engine: engine.value, acpMode: acpMode.value, acpEffort: acpEffort.value, effort: generalEffort.value, mode: mode.value, ...(cwd.value && !noWorktree.value && { worktree: worktree.value }) })
    if (cwd.value) writeJSON(RECENT, [cwd.value, ...readJSON<string[]>(RECENT, []).filter(p => p !== cwd.value)].slice(0, 20))
    text.value = ''
    files.value = []
    const { new: _new, cwd: _cwd, fork: _fork, ...rest } = route.query
    await router.replace({ query: { ...rest, session: id } })
    // This screen is gone by now: the text goes back into the session's composer.
    await store.send(id, msg, attachments).catch((err) => {
      store.drafts[id] = msg
      toast.add({ title: tt('Could not send the first message'), description: `${err instanceof Error ? err.message : String(err)}${attachments.length ? ' Attach the files again.' : ''}`, color: 'error' })
    })
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = false
  }
}

const CHIP = 'flex shrink-0 items-center gap-1.5 px-2 py-1 text-[11.5px] whitespace-nowrap hover:bg-hover hover:text-toned'
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col">
    <header
      data-tauri-drag-region
      class="flex h-11 shrink-0 items-center border-b border-default pr-4"
      :class="isDesktop() ? (collapsed ? 'pl-[92px]' : 'pl-4 max-lg:pl-[92px]') : 'pl-4'"
    >
      <h1
        data-tauri-drag-region
        class="m-0 flex-1 truncate text-[13.5px] font-semibold text-strong"
      >
        {{ forkOf ? tt('Continue here') : tt('New session') }}
      </h1>
    </header>

    <div class="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 overflow-y-auto px-5">
      <div
        v-if="forkOf"
        class="flex w-full max-w-[440px] items-center gap-2.5 border border-accented bg-surface px-3 py-2 text-[12.5px]"
      >
        <HIcon
          name="branch"
          :size="14"
          class="shrink-0 text-muted"
        />
        <span class="min-w-0 flex-1 truncate text-strong">{{ forkOf.meta.title }}</span>
        <span class="shrink-0 text-[11px] text-muted">{{ tt('from {0}', [forkOf.meta.deviceName || 'another machine']) }}</span>
      </div>
      <div class="relative w-full max-w-[440px]">
        <!-- The cat sits on the card -->
        <CatMascot class="absolute right-5 bottom-full" />
        <div
          role="radiogroup"
          :aria-label="tt('Folder')"
          class="overflow-hidden border border-accented bg-surface text-[12.5px]"
        >
          <div
            role="radio"
            :aria-checked="!cwd"
            :title="tt('The session gets a private folder of its own')"
            class="flex cursor-pointer items-center gap-2.5 border-l-2 px-3 py-2"
            :class="!cwd ? 'border-accent bg-raised text-strong' : 'border-transparent text-toned hover:bg-raised hover:text-strong'"
            @click="choose('')"
          >
            <HIcon
              name="chat"
              :size="14"
              class="shrink-0 text-muted"
            />
            <span class="min-w-0 flex-1 truncate">{{ tt('No folder') }}</span>
            <span class="shrink-0 text-[11px] text-muted">{{ tt('chat') }}</span>
          </div>
          <div
            v-for="f in folders.slice(0, 3)"
            :key="f.path"
            role="radio"
            :aria-checked="f.path === cwd"
            :title="f.path"
            class="flex cursor-pointer items-center gap-2.5 border-l-2 px-3 py-2"
            :class="f.path === cwd ? 'border-accent bg-raised text-strong' : 'border-transparent text-toned hover:bg-raised hover:text-strong'"
            @click="choose(f.path)"
          >
            <HIcon
              name="folder"
              :size="14"
              class="shrink-0 text-muted"
            />
            <span class="min-w-0 flex-1 truncate">{{ f.name }}</span>
            <span
              v-if="f.gitBranch"
              class="shrink-0 text-[11px] text-muted"
            >{{ f.gitBranch }}</span>
          </div>
          <div
            v-if="cwd && !folders.slice(0, 3).some(f => f.path === cwd)"
            role="radio"
            aria-checked="true"
            :title="cwd"
            class="flex items-center gap-2.5 border-l-2 border-accent bg-raised px-3 py-2 text-strong"
          >
            <HIcon
              name="folder"
              :size="14"
              class="shrink-0 text-muted"
            />
            <span class="min-w-0 flex-1 truncate">{{ name(cwd) }}</span>
          </div>
          <button
            type="button"
            class="flex w-full items-center gap-2.5 rounded-none border-l-2 border-transparent px-3 py-2 text-left text-toned hover:bg-raised hover:text-strong"
            @click="browse"
          >
            <HIcon
              name="plus"
              :size="14"
              class="shrink-0 text-muted"
            />{{ tt('Choose folder…') }}
          </button>
          <UPopover
            v-if="cwd"
            :open="menu === 'env-centre'"
            :content="{ side: 'bottom', align: 'start', sideOffset: 4 }"
            @update:open="menu = $event ? 'env-centre' : ''"
          >
            <button
              type="button"
              class="flex w-full items-center gap-1.5 border-t border-default px-3 py-2 text-left text-toned hover:text-strong"
            >
              {{ tt('Choose environment') }}<HIcon
                name="down"
                :size="11"
              />
            </button>
            <template #content>
              <NewEnvMenu @close="menu = ''" />
            </template>
          </UPopover>
        </div>
      </div>

      <button
        v-if="cwd && !noWorktree"
        type="button"
        role="switch"
        :aria-checked="worktree"
        class="flex w-full max-w-[440px] items-center gap-2.5 border px-3 py-2 text-left text-[12.5px]"
        :class="worktree ? 'border-accent bg-raised' : 'border-accented bg-surface hover:bg-raised'"
        @click="worktree = !worktree"
      >
        <HIcon
          :name="worktree ? 'checkbox-checked' : 'checkbox'"
          :size="14"
          :class="worktree ? 'text-accent' : 'text-muted'"
        />
        <span class="text-strong">{{ worktree ? tt('Worktree on') : tt('Turn on Worktree') }}</span>
        <span class="min-w-0 truncate text-muted">{{ worktree ? `new branch from ${baseRef || current || 'HEAD'}` : tt('Session running here') }}</span>
      </button>
      <NewProjectImport :cwd="cwd" />
    </div>

    <div class="shrink-0 px-5 pb-[11px]">
      <div class="mx-auto flex max-w-[790px] flex-col gap-1.5">
        <ComposerBox
          ref="box"
          v-model:text="text"
          v-model:files="files"
          :commands="commands"
          :no-commands="engine ? 'Agent commands appear once the session starts' : undefined"
          :suggest="suggest"
          :placeholder="tt('Message agent · / commands · @ files')"
          :cwd="cwd"
          :sending="busy"
          :error="!!error"
          send-label="Start session"
          @submit="start"
          @retry="start"
        >
          <template
            v-if="error"
            #top
          >
            <p class="m-0 border border-bad px-2.5 py-1.5 text-[12px] text-bad">
              {{ error }}
            </p>
          </template>
          <template #bottom>
            <div class="flex min-w-0 items-center gap-1 overflow-hidden text-muted">
              <UPopover
                :open="menu === 'env'"
                :content="{ side: 'top', align: 'start', sideOffset: 6 }"
                @update:open="menu = $event ? 'env' : ''"
              >
                <button
                  type="button"
                  :class="[CHIP, menu === 'env' && 'bg-hover']"
                >
                  <HIcon
                    name="monitor"
                    :size="12"
                  />{{ tt('This computer') }}
                </button>
                <template #content>
                  <NewEnvMenu @close="menu = ''" />
                </template>
              </UPopover>

              <UPopover
                :open="menu === 'folder'"
                :content="{ side: 'top', align: 'start', sideOffset: 6 }"
                @update:open="menu = $event ? 'folder' : ''"
              >
                <button
                  type="button"
                  :title="cwd || tt('No folder: the session gets a private folder of its own')"
                  :class="[CHIP, menu === 'folder' && 'bg-hover']"
                >
                  <HIcon
                    :name="cwd ? 'folder' : 'chat'"
                    :size="12"
                  /><span class="max-w-[160px] truncate text-strong">{{ cwd ? name(cwd) : tt('No folder') }}</span>
                </button>
                <template #content>
                  <NewFolderMenu
                    :folders="folders"
                    :cwd="cwd"
                    :branches="branches"
                    :current="current"
                    :base-ref="baseRef"
                    :worktree="worktree"
                    @select="choose"
                    @browse="browse"
                    @branch="baseRef = $event"
                  />
                </template>
              </UPopover>

              <button
                v-if="cwd && !noWorktree"
                type="button"
                role="switch"
                :aria-checked="worktree"
                :class="[CHIP, 'border-transparent']"
                @click="worktree = !worktree"
              >
                <HIcon
                  :name="worktree ? 'checkbox-checked' : 'checkbox'"
                  :size="12"
                  :class="worktree && 'text-accent'"
                /><span :class="worktree && 'text-strong'">{{ tt('Worktree') }}</span>
              </button>

              <span class="flex-1" />

              <SessionControls
                :model="engine ? model : model || store.models[0]?.id || ''"
                :model-engine="engine"
                :label="modelLabel"
                :scope="scope"
                :efforts="effortChoice.levels"
                :effort="effortChoice.current"
                :recommended="effortChoice.recommended"
                :mode="mode"
                :acp-modes="acpModes"
                @update:model="pickModel"
                @update:effort="pickEffort"
                @update:mode="mode = $event"
                @update:acp-mode="acpMode = $event"
              />
            </div>
          </template>
        </ComposerBox>
      </div>
    </div>

    <UModal
      v-model:open="browsing"
      :ui="{ content: 'w-auto max-w-none bg-transparent ring-0' }"
    >
      <template #content>
        <NewDirectoryBrowser
          :start="cwd"
          @pick="choose"
          @close="browsing = false"
        />
      </template>
    </UModal>
  </div>
</template>
