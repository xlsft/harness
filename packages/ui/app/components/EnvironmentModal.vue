<script setup lang="ts">
// Edit environment: the session folder's environment (lib/environment) — a name, the folders (the primary one
// and additional working directories, each with the branch a worktree starts from and the Worktree default),
// and environment variables in .env form on a second screen. Save applies it to the open sessions in the folder
// from their next tool call; new sessions there start with it.
import { isDesktop, pickFolder } from '~/lib/desktop'
import { parseEnv } from '~/lib/environment/dotenv'
import { EnvironmentParseError, type EnvironmentView } from '~/lib/environment/types'
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()
const open = defineModel<boolean>('open', { default: false })
const store = useSessionStore()
const toast = useToast()

interface FolderRow { path: string, worktree: boolean, baseRef: string, primary: boolean, exists: boolean }
const view = ref<EnvironmentView | null>(null)
const name = ref('')
const folders = ref<FolderRow[]>([])
const env = ref('')
const screen = ref<'main' | 'env'>('main')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const serverErrors = ref<{ line: number, message: string }[]>([])
const branches = ref<Record<string, { value: string, label: string, hint?: string }[]>>({})
const browsing = ref(false)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}
const base = (p: string) => p.replace(/\/+$/, '').split('/').pop() || p

async function load() {
  if (!store.transport) return
  loading.value = true
  error.value = ''
  serverErrors.value = []
  screen.value = 'main'
  try {
    const v = await store.whenOpen(props.session.meta.id, () => store.transport!.environment().get({ sessionId: props.session.meta.id }))
    view.value = v
    name.value = v.name || base(v.folder)
    env.value = v.env
    folders.value = [
      { path: v.folder, worktree: v.worktree, baseRef: v.baseRef, primary: true, exists: true },
      ...v.additionalFolders.map(f => ({ path: f.path, worktree: f.worktree, baseRef: f.baseRef, primary: false, exists: f.exists }))
    ]
    for (const f of folders.value) loadBranches(f.path)
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}
watch(open, v => v && load(), { immediate: true })

// Branches a worktree can start from; the folder's current branch first ('' = it).
async function loadBranches(path: string) {
  if (!store.transport || branches.value[path]) return
  try {
    const res = await store.transport.listBranches(path)
    branches.value[path] = [
      { value: '', label: res.current || 'current', hint: 'default' },
      ...res.branches.filter(b => b.name !== res.current).map(b => ({ value: b.name, label: b.name }))
    ]
  } catch {
    branches.value[path] = []
  }
}

async function addFolder(path: string | null) {
  browsing.value = false
  if (!path || folders.value.some(f => f.path === path)) return
  folders.value.push({ path, worktree: false, baseRef: '', primary: false, exists: true })
  loadBranches(path)
}
async function browse() {
  if (isDesktop()) addFolder(await pickFolder(folders.value[0]?.path))
  else browsing.value = true
}
function removeFolder(path: string) {
  folders.value = folders.value.filter(f => f.primary || f.path !== path)
}

// .env problems while typing (the client checks again on save).
const envErrors = computed(() => serverErrors.value.length ? serverErrors.value : parseEnv(env.value).errors)
const varCount = computed(() => parseEnv(env.value).vars.length)
watch(env, () => (serverErrors.value = []))

async function save() {
  if (!store.transport || !view.value || envErrors.value.length) {
    if (envErrors.value.length) screen.value = 'env'
    return
  }
  saving.value = true
  error.value = ''
  try {
    const [primary, ...rest] = folders.value
    const res = await store.transport.environment().update({
      folder: view.value.folder,
      name: name.value.trim(),
      worktree: primary!.worktree,
      baseRef: primary!.baseRef,
      additionalFolders: rest.map(f => ({ path: f.path, worktree: f.worktree, baseRef: f.baseRef })),
      env: env.value
    }, { sessionId: props.session.meta.id })
    const n = res.appliedSessionIds.length
    toast.add({ title: 'Environment saved', description: n ? `Applied to ${n} open ${n === 1 ? 'session' : 'sessions'} from their next step.` : 'New sessions in this folder start with it.', color: 'neutral' })
    open.value = false
  } catch (e) {
    if (e instanceof EnvironmentParseError) {
      serverErrors.value = e.errors
      screen.value = 'env'
    } else error.value = message(e)
  } finally {
    saving.value = false
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key !== 'Escape' || document.querySelector('[data-reka-popper-content-wrapper]')) return
  if (screen.value === 'env') screen.value = 'main'
  else open.value = false
}
watch(open, (v) => {
  if (v) document.addEventListener('keydown', onKey)
  else document.removeEventListener('keydown', onKey)
}, { immediate: true })
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))

const PLACEHOLDER = 'NODE_ENV=production\nGIT_AUTHOR_NAME=Your Name\n\n# Multiline values: wrap in quotes\nCONFIG="key1=val1\nkey2=val2"'
const input = 'h-9 w-full border border-accented bg-default px-3 text-[13px] text-strong outline-none focus:border-accent'
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-40 flex items-center justify-center bg-ground/60 p-6"
      @pointerdown.self="open = false"
    >
      <div
        role="dialog"
        aria-modal="true"
        :aria-label="screen === 'env' ? 'Environment variables' : 'Edit environment'"
        class="flex max-h-full w-[min(540px,100%)] flex-col overflow-hidden rounded-panel border border-line-strong bg-default"
      >
        <div class="flex h-12 shrink-0 items-center gap-2 pr-2 pl-5">
          <HIconBtn
            v-if="screen === 'env'"
            icon="arrow-left"
            label="Back"
            class="-ml-2"
            @click="screen = 'main'"
          />
          <h2 class="m-0 flex-1 text-[14px] font-semibold text-strong">
            {{ screen === 'env' ? 'Environment variables' : 'Edit environment' }}
          </h2>
          <HIconBtn
            icon="close"
            label="Close"
            kbd="esc"
            @click="open = false"
          />
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 pb-4">
          <p
            v-if="loading"
            class="m-0 py-6 text-center text-[12.5px] text-muted"
          >
            Loading…
          </p>
          <p
            v-else-if="view?.readOnly"
            class="m-0 py-6 text-center text-[12.5px] text-muted"
          >
            This session runs on {{ session.meta.deviceName || 'another computer' }}; its environment is edited there.
          </p>

          <template v-else-if="view && screen === 'main'">
            <label class="flex flex-col gap-1.5 text-[12.5px] text-strong">
              Name
              <input
                v-model="name"
                :class="input"
                autocapitalize="off"
                autocorrect="off"
                spellcheck="false"
                maxlength="80"
              >
            </label>

            <div class="mt-4 mb-1.5 flex items-center text-[12.5px] text-strong">
              <span class="flex-1">Folders</span>
              <HBtn
                size="md"
                @click="browse"
              >
                <HIcon
                  name="plus"
                  :size="12"
                />Add
              </HBtn>
            </div>
            <div class="overflow-hidden border border-line">
              <div
                v-for="f in folders"
                :key="f.path"
                class="flex items-center gap-2.5 border-b border-line px-3 py-2 text-[12.5px] last:border-b-0"
              >
                <HIcon
                  name="folder"
                  class="shrink-0 text-muted"
                />
                <span
                  class="min-w-0 flex-1 truncate"
                  :class="f.exists ? 'text-strong' : 'text-bad'"
                  :title="f.path"
                >{{ base(f.path) }}<span
                  v-if="!f.exists"
                  class="ml-1.5 text-[11px]"
                >missing</span></span>
                <div
                  v-if="branches[f.path]?.length"
                  class="w-[150px] shrink-0"
                >
                  <HSelect
                    v-model="f.baseRef"
                    :options="branches[f.path]!"
                    :aria-label="`Branch a worktree of ${base(f.path)} starts from`"
                  />
                </div>
                <label class="flex shrink-0 items-center gap-1.5 text-[12px] text-muted">
                  <input
                    v-model="f.worktree"
                    type="checkbox"
                  >Worktree
                </label>
                <HIconBtn
                  v-if="!f.primary"
                  icon="close"
                  :label="`Remove ${base(f.path)}`"
                  :size="26"
                  :icon-size="12"
                  @click="removeFolder(f.path)"
                />
              </div>
            </div>

            <button
              type="button"
              class="mt-4 flex w-full items-center gap-3 border border-line px-4 py-3 text-left hover:bg-hover"
              @click="screen = 'env'"
            >
              <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                <span class="text-[13px] text-strong">Environment variables</span>
                <span
                  class="truncate text-[12px]"
                  :class="envErrors.length ? 'text-bad' : 'text-muted'"
                >{{ envErrors.length ? `${envErrors.length} ${envErrors.length === 1 ? 'problem' : 'problems'} to fix` : varCount ? `${varCount} set for this environment` : 'None for this environment' }}</span>
              </span>
              <HIcon
                name="right"
                class="text-muted"
              />
            </button>
          </template>

          <template v-else-if="view && screen === 'env'">
            <p class="m-0 mb-3 text-[12.5px] text-muted">
              Set for sessions started in {{ name || base(view.folder) }}, and for the open ones from their next step.
            </p>
            <textarea
              v-model="env"
              :placeholder="PLACEHOLDER"
              aria-label="Environment variables"
              autocapitalize="off"
              autocorrect="off"
              spellcheck="false"
              rows="9"
              class="w-full resize-y border border-accented bg-default px-3 py-2.5 font-mono text-[12.5px] leading-[1.6] text-strong outline-none placeholder:text-dimmed focus:border-accent"
            />
            <ul
              v-if="envErrors.length"
              class="m-0 mt-2 flex list-none flex-col gap-0.5 p-0 text-[12px] text-bad"
            >
              <li
                v-for="e in envErrors"
                :key="e.line + e.message"
              >
                Line {{ e.line }}: {{ e.message }}
              </li>
            </ul>
            <p class="m-0 mt-2 text-[12px] text-muted">
              In .env format. Stored encrypted on this computer and passed to sessions' commands; never sent to the server.
            </p>
          </template>

          <p
            v-if="error"
            class="m-0 mt-3 border border-bad px-3 py-2 text-[12px] text-bad"
          >
            {{ error }}
          </p>
        </div>

        <div
          v-if="view && !view.readOnly"
          class="flex shrink-0 justify-end gap-2 border-t border-line px-5 py-3"
        >
          <template v-if="screen === 'main'">
            <HBtn
              size="lg"
              @click="open = false"
            >
              Cancel
            </HBtn>
            <HBtn
              size="lg"
              variant="primary"
              :disabled="saving"
              @click="save"
            >
              {{ saving ? 'Saving…' : 'Save' }}
            </HBtn>
          </template>
          <HBtn
            v-else
            size="lg"
            variant="primary"
            @click="screen = 'main'"
          >
            Done
          </HBtn>
        </div>
      </div>
    </div>
    <UModal
      v-model:open="browsing"
      :ui="{ content: 'w-auto max-w-none bg-transparent ring-0' }"
    >
      <template #content>
        <NewDirectoryBrowser
          :start="folders[0]?.path ?? ''"
          @pick="addFolder"
          @close="browsing = false"
        />
      </template>
    </UModal>
  </Teleport>
</template>
