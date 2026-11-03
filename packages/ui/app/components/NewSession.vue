<script setup lang="ts">
// New session screen (like Claude Code's): pick where the session runs, then type the first message.
// Centre: recent folders, environment, the worktree switch. Bottom: the composer with chips for environment,
// folder (recent / Open folder… / branch), worktree, model, effort and mode. The session is created on the
// client with the first message. Shown for ?new=1 (sidebar New session, ⌘N) and on a client with no sessions.
// With ?fork=<id> ("Continue here" on a session of another machine) the new session starts from a copy of that
// session's transcript, in the folder picked here.
import type { Branch, RecentFolder } from '~/gen/harness/agent/v1/agent_pb'
import type { EffortName, PermissionModeName } from '~/lib/session/types'
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
const effort = ref<EffortName>('medium')
const mode = ref<PermissionModeName>('auto')
const text = ref('')
const busy = ref(false)
const error = ref('')
// One open menu at a time; the environment menu has two triggers (centre card and footer chip).
const menu = ref<'' | 'env' | 'env-centre' | 'folder' | 'model' | 'effort' | 'mode'>('')
const browsing = ref(false)
const input = ref<HTMLTextAreaElement | null>(null)

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
  const prefs = readJSON<{ cwd?: string, worktree?: boolean, model?: string, effort?: EffortName, mode?: PermissionModeName }>(PREFS, {})
  worktree.value = prefs.worktree ?? false
  model.value = prefs.model ?? ''
  effort.value = prefs.effort ?? 'medium'
  mode.value = prefs.mode ?? 'auto'
  await loadFolders()
  const asked = typeof route.query.cwd === 'string' ? route.query.cwd : ''
  cwd.value = asked || prefs.cwd || folders.value[0]?.path || ''
  if (asked && !folders.value.some(f => f.path === asked)) folders.value.unshift({ path: asked, name: name(asked), exists: true } as RecentFolder)
  input.value?.focus()
})

// "+" next to a sidebar group while this screen is already open.
watch(() => route.query.cwd, (asked) => {
  if (typeof asked === 'string' && asked) choose(asked)
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

function choose(path: string) {
  cwd.value = path
  menu.value = ''
  browsing.value = false
  if (!folders.value.some(f => f.path === path)) folders.value.unshift({ path, name: name(path), exists: true } as RecentFolder)
}

const modelLabel = computed(() => model.value || store.models[0]?.id || 'default model')
const efforts = computed(() => modelEfforts(store.models.find(m => m.id === (model.value || store.models[0]?.id))))
// Each model starts at its own remembered effort (Settings → Models), else the nearest level it offers.
watch([model, efforts], () => {
  const own = readJSON<{ effortByModel?: Record<string, EffortName> }>(PREFS, {}).effortByModel?.[model.value || store.models[0]?.id || '']
  effort.value = fitEffort(own ?? effort.value, efforts.value) ?? effort.value
})

async function start() {
  const msg = text.value.trim()
  if (!msg || !cwd.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const id = await store.createSession({
      cwd: cwd.value,
      model: model.value,
      effort: efforts.value.length ? effort.value : undefined,
      mode: mode.value,
      worktree: worktree.value ? { baseRef: baseRef.value } : undefined,
      forkOf: forkOf.value?.meta.id
    })
    writeJSON(PREFS, { ...readJSON<object>(PREFS, {}), cwd: cwd.value, worktree: worktree.value, model: model.value, effort: effort.value, mode: mode.value })
    writeJSON(RECENT, [cwd.value, ...readJSON<string[]>(RECENT, []).filter(p => p !== cwd.value)].slice(0, 20))
    text.value = ''
    const { new: _new, cwd: _cwd, fork: _fork, ...rest } = route.query
    await router.replace({ query: { ...rest, session: id } })
    await store.send(id, msg)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    start()
  }
}

const CHIP = 'flex shrink-0 items-center gap-1.5 border px-1.5 py-0.5 text-[11.5px] whitespace-nowrap hover:text-toned'
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
        class="m-0 flex-1 truncate text-[13.5px] font-semibold text-white"
      >
        {{ forkOf ? 'Continue here' : 'New session' }}
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
        <span class="min-w-0 flex-1 truncate text-white">{{ forkOf.meta.title }}</span>
        <span class="shrink-0 text-[11px] text-muted">from {{ forkOf.meta.deviceName || 'another machine' }}</span>
      </div>
      <div class="w-full max-w-[440px] border border-accented bg-surface text-[12.5px]">
        <div
          v-for="f in folders.slice(0, 3)"
          :key="f.path"
          :title="f.path"
          class="flex cursor-default items-center gap-2.5 border-l-2 px-3 py-2"
          :class="f.path === cwd ? 'border-accent bg-raised text-white' : 'border-transparent text-toned hover:bg-raised hover:text-white'"
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
          v-if="!folders.length"
          class="px-3 py-2 text-muted"
        >
          No recent folders: pick one below.
        </div>
        <UPopover
          :open="menu === 'env-centre'"
          :content="{ side: 'bottom', align: 'start', sideOffset: 4 }"
          @update:open="menu = $event ? 'env-centre' : ''"
        >
          <button
            type="button"
            class="flex w-full items-center gap-1.5 border-t border-default px-3 py-2 text-left text-toned hover:text-white"
          >
            Choose environment<HIcon
              name="down"
              :size="11"
            />
          </button>
          <template #content>
            <NewEnvMenu @close="menu = ''" />
          </template>
        </UPopover>
      </div>

      <button
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
        <span class="text-white">{{ worktree ? 'Worktree on' : 'Turn on Worktree' }}</span>
        <span class="min-w-0 truncate text-muted">{{ worktree ? `new branch from ${baseRef || current || 'HEAD'}` : 'Session running here' }}</span>
      </button>
    </div>

    <div class="shrink-0 px-5 pb-[11px]">
      <div class="mx-auto flex max-w-[790px] flex-col gap-1.5">
        <p
          v-if="error"
          class="m-0 border border-bad px-2.5 py-1.5 text-[12px] text-bad"
        >
          {{ error }}
        </p>
        <div
          class="flex min-h-[42px] items-center gap-1.5 border bg-surface px-2.5"
          :class="text ? 'border-accent' : 'border-accented'"
        >
          <HIcon
            name="plus"
            class="shrink-0 text-muted"
          />
          <textarea
            ref="input"
            v-model="text"
            rows="1"
            aria-label="First message"
            placeholder="Describe a task or ask a question"
            class="min-w-0 flex-1 resize-none overflow-hidden bg-transparent py-[3px] text-[13px] leading-[1.6] text-white outline-none placeholder:truncate placeholder:text-muted"
            @keydown="onKeydown"
          />
          <button
            type="button"
            aria-label="Start session"
            :disabled="!text.trim() || !cwd || busy"
            class="flex size-7 shrink-0 items-center justify-center bg-accent text-white disabled:bg-raised disabled:text-muted"
            @click="start"
          >
            <HIcon
              :name="busy ? 'loading' : 'up'"
              :spin="busy"
            />
          </button>
        </div>

        <div class="flex min-w-0 items-center gap-1 overflow-hidden text-muted">
          <UPopover
            :open="menu === 'env'"
            :content="{ side: 'top', align: 'start', sideOffset: 6 }"
            @update:open="menu = $event ? 'env' : ''"
          >
            <button
              type="button"
              :class="[CHIP, menu === 'env' ? 'border-accent' : 'border-transparent']"
            >
              <HIcon
                name="monitor"
                :size="12"
              />This computer
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
              :title="cwd"
              :class="[CHIP, menu === 'folder' ? 'border-accent' : 'border-transparent']"
            >
              <HIcon
                name="folder"
                :size="12"
              /><span class="max-w-[160px] truncate text-white">{{ cwd ? name(cwd) : 'Choose folder' }}</span>
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
            /><span :class="worktree && 'text-white'">Worktree</span>
          </button>

          <span class="flex-1" />

          <UPopover
            :open="menu === 'model'"
            :content="{ side: 'top', align: 'end', sideOffset: 6 }"
            @update:open="menu = $event ? 'model' : ''"
          >
            <button
              type="button"
              :class="[CHIP, menu === 'model' ? 'border-accent' : 'border-transparent']"
            >
              <span class="text-white">{{ modelLabel }}</span>
            </button>
            <template #content>
              <div
                role="listbox"
                aria-label="Model"
                class="w-[380px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
              >
                <ModelList
                  :selected="model || store.models[0]?.id || ''"
                  @pick="model = $event; menu = ''"
                />
              </div>
            </template>
          </UPopover>

          <UPopover
            v-if="efforts.length"
            :open="menu === 'effort'"
            :content="{ side: 'top', align: 'end', sideOffset: 6 }"
            @update:open="menu = $event ? 'effort' : ''"
          >
            <button
              type="button"
              aria-label="Effort"
              :class="[CHIP, menu === 'effort' ? 'border-accent' : 'border-transparent', '@max-[560px]:hidden']"
            >
              effort <span :class="effort === efforts.at(-1) && efforts.length > 1 ? 'text-accent' : 'text-white'">{{ effort }}</span>
            </button>
            <template #content>
              <div class="border border-accented bg-hover">
                <EffortSlider
                  v-model="effort"
                  :levels="efforts"
                />
              </div>
            </template>
          </UPopover>

          <UPopover
            :open="menu === 'mode'"
            :content="{ side: 'top', align: 'end', sideOffset: 6 }"
            @update:open="menu = $event ? 'mode' : ''"
          >
            <button
              type="button"
              :class="[CHIP, menu === 'mode' ? 'border-accent' : 'border-transparent']"
            >
              <span class="text-white">{{ MODE_LABEL[mode] }}</span>
            </button>
            <template #content>
              <div
                role="listbox"
                aria-label="Permission mode"
                class="w-[280px] border border-accented bg-hover text-[12.5px]"
              >
                <div
                  v-for="m in MODES.filter(x => x.key !== 'bypass')"
                  :key="m.key"
                  role="option"
                  :aria-selected="m.key === mode"
                  class="flex cursor-default flex-col border-l-2 px-2.5 py-[5px]"
                  :class="m.key === mode ? 'border-accent bg-raised' : 'border-transparent hover:bg-raised'"
                  @click="mode = m.key; menu = ''"
                >
                  <span :class="m.cls">{{ m.label }}</span><span class="text-[11px] text-muted">{{ m.detail }}</span>
                </div>
              </div>
            </template>
          </UPopover>
        </div>
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
