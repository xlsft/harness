<script setup lang="ts">
// A plugin's pane (docs/design/plugin-ui.md): the page the client built, in a frame sandboxed with scripts only. The page talks
// to the app only through window.harness; each call goes to the client, which checks the capability was declared and approved
// on this device. A capability not yet decided on is asked for here, outside the frame, where the page can't reach.
import type { SessionState } from '~/lib/session/types'
import { capabilityLabel, type PluginPaneDocVM } from '~/lib/plugins/types'
import { pluginPaneDocument, themeStyle, PLUGIN_UI_VERSION, type PaneRequest } from '~/lib/plugins/ui'
import { themeTokens } from '~/lib/visual'
import { newUuid } from '~/stores/sessions'

const props = defineProps<{ session: SessionState, menu: string }>()
const { closePane, openPane } = usePanes()
const { open, refresh } = usePluginPane()
const store = useSessionStore()
const { theme } = useAppearance()
const filesOpen = useState<string>('files-open-path', () => '')

const sid = computed(() => props.session.meta.id)
const current = computed(() => open.value[sid.value] ?? null)
const api = () => store.transport!.plugins()

const doc = ref<PluginPaneDocVM | null>(null)
const problem = ref('')
const nonce = ref(newUuid())
const srcdoc = ref('')
// The frame left its page (a link, location): it is stopped, since a navigation can carry what the page read.
const stopped = ref(false)
const frame = ref<HTMLIFrameElement | null>(null)
const networkLater = ref(false)
let loads = 0

// What this pane instance learned: capabilities that answered, ones the user refused (not asked again until a reload), and
// whether the page listens for session changes.
const granted = new Set<string>()
const denied = new Set<string>()
let sessionListener = false

interface Ask { capability: string, resolve: (allow: boolean) => void }
const asks = ref<Ask[]>([])
const ask = computed(() => asks.value[0] ?? null)

async function load() {
  doc.value = null
  problem.value = ''
  srcdoc.value = ''
  stopped.value = false
  networkLater.value = false
  loads = 0
  granted.clear()
  denied.clear()
  sessionListener = false
  for (const a of asks.value) a.resolve(false)
  asks.value = []
  const c = current.value
  if (!c || !store.transport) return
  try {
    const d = await api().getPane(c.pluginId, c.paneId, sid.value)
    nonce.value = newUuid()
    doc.value = d
    srcdoc.value = pluginPaneDocument(d.html, nonce.value)
  } catch (e) {
    problem.value = e instanceof Error ? e.message : String(e)
  }
}
watch(() => [current.value?.pluginId, current.value?.paneId, sid.value], load, { immediate: true })

function onLoad() {
  loads++
  if (loads > 1) {
    stopped.value = true
    srcdoc.value = ''
  }
}

function post(m: Record<string, unknown>) {
  frame.value?.contentWindow?.postMessage({ harnessHost: PLUGIN_UI_VERSION, ...m }, '*')
}

class CallError extends Error {
  constructor(public code: string, message: string) {
    super(message)
  }
}

function askFor(capability: string): Promise<boolean> {
  return new Promise(resolve => asks.value = [...asks.value, { capability, resolve }])
}

async function decide(allow: boolean) {
  const a = ask.value
  if (!a || !doc.value || !current.value) return
  if (allow) {
    try {
      await api().setUiGrant(current.value.pluginId, a.capability, true, doc.value.contentHash)
      refresh()
    } catch (e) {
      problem.value = e instanceof Error ? e.message : String(e)
      allow = false
    }
  }
  // One answer settles every waiting call for the same capability.
  const same = asks.value.filter(x => x.capability === a.capability)
  asks.value = asks.value.filter(x => x.capability !== a.capability)
  for (const x of same) x.resolve(allow)
}

async function call(method: string, params: unknown, active: boolean): Promise<unknown> {
  const c = current.value
  if (!c) throw new CallError('closed', 'the pane is closed')
  // The frame's own word that a person just acted: a speed bump against a page that sends on its own, not a proof.
  if (method === 'session.prompt' && !active) throw new CallError('needs_user_action', 'session.prompt works right after a click or a key press in the pane')
  for (let attempt = 0; attempt < 2; attempt++) {
    const r = await api().callUi(c.pluginId, sid.value, method, JSON.stringify(params ?? {}))
    if (r.needsApproval) {
      const cap = r.needsApproval
      if (denied.has(cap) || attempt > 0) throw new CallError('denied', `${cap} was not allowed`)
      if (!await askFor(cap)) {
        denied.add(cap)
        throw new CallError('denied', `${cap} was not allowed`)
      }
      continue
    }
    granted.add(method)
    if (r.openFile) {
      filesOpen.value = r.openFile
      openPane('files')
    }
    if (r.theme) return themeTokens()
    return r.resultJson ? JSON.parse(r.resultJson) : null
  }
  throw new CallError('denied', `${method} was not allowed`)
}

function onMessage(e: MessageEvent) {
  const d = e.data as PaneRequest
  if (!d || d.harnessPlugin !== nonce.value || e.source !== frame.value?.contentWindow) return
  if (d.subscribe === 'session') {
    sessionListener = true
    return
  }
  if (typeof d.id !== 'number' || typeof d.method !== 'string') return
  const id = d.id
  if (d.v !== PLUGIN_UI_VERSION) {
    post({ id, error: { code: 'unsupported_version', message: `this app speaks version ${PLUGIN_UI_VERSION} of the message API` } })
    return
  }
  call(d.method, d.params, d.active === true).then(
    result => post({ id, result }),
    (err: unknown) => post({ id, error: { code: err instanceof CallError ? err.code : 'failed', message: err instanceof Error ? err.message : String(err) } })
  )
}
onMounted(() => window.addEventListener('message', onMessage))
onBeforeUnmount(() => {
  window.removeEventListener('message', onMessage)
  for (const a of asks.value) a.resolve(false)
})

// The theme follows without a reload: its variables always, and its tokens for a page that read them.
watch(theme, () => {
  post({ event: 'theme-vars', css: themeStyle() })
  if (granted.has('theme.read')) post({ event: 'theme', data: themeTokens() })
})

// A page that listens for the session hears of it when its title, model, mode or status change: the client answers each time.
watch(() => [props.session.meta.title, props.session.meta.model, props.session.meta.permissionMode, props.session.meta.status], async () => {
  const c = current.value
  if (!sessionListener || !granted.has('session.read') || !c) return
  try {
    const r = await api().callUi(c.pluginId, sid.value, 'session.read', '{}')
    if (r.resultJson) post({ event: 'session', data: JSON.parse(r.resultJson) })
  } catch { /* the next change tries again */ }
})

async function allowNetwork() {
  if (!doc.value || !current.value) return
  try {
    await api().setUiGrant(current.value.pluginId, 'network', true, doc.value.contentHash)
    refresh()
    await load()
  } catch (e) {
    problem.value = e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <section
    :aria-label="current?.title ?? tt('Plugin')"
    class="flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-[2px] border-b border-line pr-1.5 pl-3.5">
      <span class="min-w-0 flex-1 truncate font-display text-[13px] text-strong">
        {{ current?.title ?? tt('Plugin') }}<span
          v-if="doc"
          class="ml-2 text-[11.5px] text-muted"
        >{{ doc.pluginName }}</span>
      </span>
      <PaneBtn
        icon="reload"
        :label="tt('Reload')"
        @click="load"
      />
      <PaneFullBtn />
      <PaneBtn
        icon="close"
        :label="tt('Close')"
        @click="closePane('plugin')"
      />
    </div>

    <div
      v-if="ask && doc"
      role="alertdialog"
      :aria-label="tt('Allow {0}?', [doc.pluginName])"
      class="flex shrink-0 flex-col gap-2 border-b border-line bg-surface px-3.5 py-2.5 text-[12.5px]"
      data-plugin-ask
    >
      <p class="m-0 text-toned">
        {{ tt('{0} asks to:', [doc.pluginName]) }} <span class="text-strong">{{ capabilityLabel(ask.capability) }}</span>
      </p>
      <p class="m-0 text-[11.5px] text-muted">
        {{ tt('On this device only, for this version of the plugin. Revoke it in Extensions.') }}
      </p>
      <div class="flex gap-1.5">
        <HBtn
          variant="primary"
          size="sm"
          @click="decide(true)"
        >
          {{ tt('Allow') }}
        </HBtn>
        <HBtn
          size="sm"
          @click="decide(false)"
        >
          {{ tt('Don\'t allow') }}
        </HBtn>
      </div>
    </div>

    <div
      v-if="doc?.networkPending.length && !networkLater && !stopped"
      class="flex shrink-0 flex-wrap items-center gap-2 border-b border-line px-3.5 py-2 text-[12px]"
      data-plugin-network
    >
      <span class="min-w-0 flex-1 text-toned">{{ tt('{0} wants to reach {1}. It runs offline until you allow it.', [doc.pluginName, doc.networkPending.join(', ')]) }}</span>
      <HBtn
        size="xs"
        @click="allowNetwork"
      >
        {{ tt('Allow') }}
      </HBtn>
      <HBtn
        size="xs"
        variant="ghost"
        tone="muted"
        @click="networkLater = true"
      >
        {{ tt('Not now') }}
      </HBtn>
    </div>

    <iframe
      v-if="srcdoc"
      ref="frame"
      :key="nonce"
      :srcdoc="srcdoc"
      :title="current?.title"
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      class="min-h-0 w-full flex-1 border-0"
      @load="onLoad"
    />
    <div
      v-else
      class="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center text-[12.5px] text-muted"
    >
      <template v-if="stopped">
        <p class="m-0">
          {{ tt('This pane tried to leave its page and was stopped.') }}
        </p>
        <HBtn
          size="sm"
          @click="load"
        >
          {{ tt('Reload') }}
        </HBtn>
      </template>
      <p
        v-else-if="problem"
        class="m-0 text-bad"
      >
        {{ problem }}
      </p>
      <p
        v-else-if="!current"
        class="m-0"
      >
        {{ tt('No plugin pane open') }}
      </p>
    </div>
  </section>
</template>
