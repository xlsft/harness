<script setup lang="ts">
// Composer (Composer.dc.html): Enter sends, Shift+Enter new line, Cmd/Ctrl+Enter queues; while a turn runs, messages queue (the
// transcript shows them with their actions: send now, cancel, click to edit) and Stop interrupts. `/` and `@` open
// menus, `$` (or `!`) runs a shell command in the session's shell on the client (RunShellCommand: at once, no turn;
// the model sees it next), `#` saves to memory; `+` opens a menu for these. Status line, store drafts (rewind, fork,
// edit a queued message), attachments, drop, voice, context warning, permission waiting, offline and ended states.
// The field itself is ComposerBox, shared with the new-session screen; this is what it means in a session.
import type { Hint } from '~/components/composer/ComposerFooter.vue'
import { readAttachments } from '~/lib/session/attachments'
import type { SessionState } from '~/lib/session/types'
import type { ComposerFile, ComposerMode, ComposerPreset, MenuOption } from '~/utils/composer'

const props = defineProps<{ session: SessionState, preset?: ComposerPreset }>()
const store = useSessionStore()

const box = ref<{ focusEnd: () => void } | null>(null)
const text = ref(props.preset?.text ?? '')
const focusEnd = () => box.value?.focusEnd()

// "Attach as context" from other panes: each path becomes an @mention at the end of the draft.
const attach = useState<string[]>('composer-attach', () => [])
watch(attach, (paths) => {
  if (!paths.length) return
  const mentions = paths.map(p => `@${p}`).join(' ')
  text.value = text.value && !text.value.endsWith(' ') ? `${text.value} ${mentions} ` : `${text.value}${mentions} `
  attach.value = []
  nextTick(focusEnd)
}, { deep: true })
// "Reply" on text picked in the transcript: a Markdown quote ("> …" and a blank line) at the end of the text, so
// quotes and answers can alternate; the field shows quote lines as quotes.
const { reply } = useQuoteRequests()
watch(reply, (q) => {
  if (!q) return
  const before = text.value.replace(/\s*$/, '')
  text.value = `${before ? `${before}\n\n` : ''}> ${quoteLine(q)}\n\n`
  reply.value = ''
  nextTick(focusEnd)
})
const mode = ref<ComposerMode>('normal')

const error = ref(props.preset?.error ?? '')
const failedText = ref('')
// The failed submission's files, and whether it was a shell command (Retry runs it again).
const failedFiles = ref<ComposerFile[]>([])
const failedShell = ref(false)
const files = ref<ComposerFile[]>(props.preset?.files ?? [])
const memoryTarget = ref('AGENTS.md')

const id = computed(() => props.session.meta.id)

// The unsent draft of each session (text and mode) is kept in this browser: switching sessions, reloading or
// quitting the app keeps it; sending (an empty composer) clears it.
const draftKey = (sid: string) => `harness-draft:${sid}`
function loadDraft(sid: string) {
  try {
    const v = JSON.parse(localStorage.getItem(draftKey(sid)) ?? 'null') as { text?: string, mode?: ComposerMode } | null
    if (v && typeof v.text === 'string') return v
  } catch { /* storage unavailable */ }
  return null
}
function saveDraft(sid: string, t: string, m: ComposerMode) {
  try {
    if (!t && m === 'normal') localStorage.removeItem(draftKey(sid))
    else localStorage.setItem(draftKey(sid), JSON.stringify({ text: t, mode: m }))
  } catch { /* storage unavailable */ }
}
if (!props.preset) {
  const d = loadDraft(id.value)
  if (d) {
    text.value = d.text ?? ''
    if (d.mode) mode.value = d.mode
  }
}
watch(id, (now, before) => {
  if (props.preset) return
  if (before) saveDraft(before, text.value, mode.value)
  const d = loadDraft(now)
  text.value = d?.text ?? ''
  mode.value = d?.mode ?? 'normal'
})
watch([text, mode], ([t, m]) => {
  if (!props.preset) saveDraft(id.value, t, m)
})
const status = computed(() => props.session.meta.status)
const busy = computed(() => status.value === 'working' || status.value === 'needs_input')
const offline = computed(() => status.value === 'waiting_for_connection')
const ended = computed(() => status.value === 'ended')
const planMode = computed(() => props.session.meta.permissionMode === 'plan')
const queue = computed(() => props.session.queue)
// Text handed over by the store (a rewound or forked message, a queued message taken back to edit) replaces the
// draft once.
watch(() => store.drafts[id.value], (draft) => {
  if (draft === undefined) return
  text.value = draft
  Reflect.deleteProperty(store.drafts, id.value)
  nextTick(focusEnd)
}, { immediate: true })
watch(() => store.draftFiles[id.value], (back) => {
  if (!back) return
  files.value = [...files.value, ...back.map(composerFile)]
  Reflect.deleteProperty(store.draftFiles, id.value)
}, { immediate: true })
const multiline = computed(() => text.value.includes('\n'))
const uploading = computed(() => files.value.filter(f => f.progress !== undefined && f.progress < 100).length)

const model = computed(() => props.session.meta.engine === 'harness' ? store.models.find(m => m.id === props.session.meta.model) : undefined)
const contextPct = computed(() => {
  const agent = props.session.meta.acp
  return Math.round(((Number(agent?.contextUsedTokens ?? 0) || props.session.contextTokens) / (Number(agent?.contextWindowTokens ?? 0) || model.value?.contextWindow || 200_000)) * 100)
})

// A session on an ACP agent runs its own commands, and has no MCP prompts of ours.
const onAgent = computed(() => props.session.meta.engine !== 'harness')
// MCP prompts come from the client (ListMcpCommands) and are read again when a server says its list changed.
const mcpCommands = ref<MenuOption[]>([])
watch([id, () => props.session.mcpListVersion, onAgent], async () => {
  if (onAgent.value) {
    mcpCommands.value = []
    return
  }
  const sid = id.value
  const list = await store.mcpCommands(sid)
  if (sid === id.value) mcpCommands.value = list.map(c => ({ key: c.command, label: c.command, detail: c.description || c.title, tag: 'mcp' }))
}, { immediate: true })
// What the client reports for this session: built-ins, skills, custom commands, an ACP agent's own commands.
const slashMenu = useSlashMenu(() => ({ sessionId: id.value }), () => text.value)
const commands = computed<MenuOption[]>(() => [...slashMenu.value, ...mcpCommands.value])
const hasShell = computed(() => !props.session.meta.unsupported.includes('shell_commands'))
const hasCompaction = computed(() => !props.session.meta.unsupported.includes('compaction'))

const suggest = computed(() => {
  const sid = id.value
  return async (q: string): Promise<MenuOption[]> => [
    ...fileMentionOptions((await store.suggestFiles(sid, q || 'trans')).slice(0, 2)),
    ...(await store.mcpResources(sid, q)).map(r => ({ key: r.mention, label: r.mention, detail: r.description || r.name, tag: 'mcp' })),
    ...MENTION_EXTRAS.filter(e => !q || e.label.includes(q))
  ]
})

const placeholder = computed(() => {
  if (planMode.value && !busy.value) return 'Describe what to plan. Nothing will be changed.'
  if (queue.value.length) return `${queue.value.length} queued · type to add another`
  if (busy.value) return 'Type to queue a message for after this turn'
  return hasShell.value ? 'Message agent · / commands · @ files · $ shell' : 'Message agent · / commands · @ files'
})

// Footer hint: text segments, each optionally led by a shortcut rendered with UKbd.
const hint = computed<Hint>(() => {
  if (mode.value === 'shell') return [{ text: 'output goes to context ·' }, { keys: 'backspace', text: 'on empty to exit' }]
  if (files.value.some(f => f.error)) return [{ text: 'remove the files that cannot be sent' }]
  if (uploading.value) return [{ text: `waiting for ${uploading.value} upload${uploading.value > 1 ? 's' : ''}` }]
  if (multiline.value) return [{ keys: 'shift+enter', text: 'new line' }]
  if (planMode.value) return [{ keys: 'shift+tab', text: 'cycle mode' }]
  if (busy.value || queue.value.length) return [{ keys: 'meta+enter', text: 'queues a message' }]
  if (contextPct.value >= 90) return [{ keys: 'enter', text: 'send' }, { keys: 'meta+enter', text: 'queue for later' }]
  return [{ keys: 'enter', text: 'send' }, { keys: 'meta+enter', text: 'queue' }]
})

async function submit() {
  const body = text.value
  if ((!body.trim() && (mode.value !== 'normal' || !files.value.length)) || ended.value) return
  error.value = ''
  if (mode.value !== 'normal') {
    const route = composerRoute(mode.value, body, memoryTarget.value)
    text.value = ''
    mode.value = 'normal'
    if (route?.kind === 'shell') await safeRun(route.command)
    else if (route) await safeSend(route.text)
    return
  }
  text.value = ''
  const sent = files.value
  files.value = []
  await safeSend(body, sent)
}

async function safeSend(body: string, sent: ComposerFile[] = []) {
  failedShell.value = false
  try {
    await store.send(id.value, body, await readAttachments(sent))
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
    failedText.value = body
    failedFiles.value = sent
    text.value = body
    files.value = sent
  }
}

// Shell mode: the client runs the command right away (even while a turn runs); a refusal puts it back.
async function safeRun(command: string) {
  failedShell.value = false
  try {
    await store.runShell(id.value, command)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
    failedText.value = command
    failedShell.value = true
    text.value = command
    mode.value = 'shell'
  }
}

function retry() {
  error.value = ''
  text.value = ''
  if (failedShell.value) {
    mode.value = 'normal'
    safeRun(failedText.value)
  } else {
    files.value = []
    safeSend(failedText.value, failedFiles.value)
  }
}
</script>

<template>
  <ComposerBox
    ref="box"
    v-model:text="text"
    v-model:mode="mode"
    v-model:files="files"
    :commands="commands"
    :suggest="suggest"
    :placeholder="placeholder"
    :cwd="session.meta.cwd"
    :busy="busy"
    :plan-mode="planMode"
    :has-shell="hasShell"
    has-memory
    :error="!!error"
    :preset="preset"
    @submit="submit"
    @stop="store.interrupt(id)"
    @retry="retry"
  >
    <template #top>
      <UsageLimitBanner :session="session" />
      <GoalBanner :session="session" />
      <ComputerUseBar :session="session" />

      <!-- context almost full -->
      <div
        v-if="contextPct >= 90 && !busy && hasCompaction"
        class="flex items-center gap-[9px] border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12px]"
      >
        <HIcon
          name="warn"
          :size="14"
          class="text-warn"
        />
        <span class="text-strong">{{ tt('Context {0}% full', [contextPct]) }}</span><span class="text-muted">{{ tt('auto-compact at 95%') }}</span>
        <span class="flex-1" /><HBtn @click="store.compact(id)">
          {{ tt('Compact now') }}
        </HBtn>
      </div>
    </template>

    <template
      v-if="offline || ended"
      #instead
    >
      <div
        v-if="offline"
        aria-disabled="true"
        class="flex min-h-11 items-center gap-2 border border-default bg-default px-2.5 text-muted"
      >
        <HIcon name="wifi" />
        <span class="flex-1">{{ tt('Reconnecting to {0} client…', [session.meta.deviceName || 'the']) }}</span>
        <HBtn
          size="sm"
          @click="store.open(id)"
        >
          {{ tt('Reconnect now') }}
        </HBtn>
      </div>

      <!-- ended / read-only -->
      <div
        v-else
        class="flex min-h-11 items-center gap-2 border border-default px-2.5 text-muted"
      >
        <HIcon name="lock" />
        <span class="flex-1">{{ tt('Session ended · {0}', [session.endedReason || 'closed']) }}</span>
        <HBtn
          variant="primary"
          size="sm"
        >
          {{ tt('Fork to continue') }}
        </HBtn>
      </div>
    </template>

    <template #bottom>
      <div
        v-if="error"
        class="text-[11.5px] text-bad"
      >
        {{ tt('{0} Your message wasn\'t sent.', [error]) }}
      </div>
      <div
        v-if="mode === 'memory'"
        class="flex flex-wrap gap-2 text-[11.5px] text-muted"
      >
        <span>{{ tt('save to') }}</span>
        <button
          v-for="t in ['AGENTS.md', 'user memory', '.harness/rules']"
          :key="t"
          type="button"
          class="border px-1.5"
          :class="memoryTarget === t ? 'border-pink text-strong' : 'border-default'"
          @click="memoryTarget = t"
        >
          {{ t }}
        </button>
      </div>

      <ComposerFooter
        :session="session"
        :hint="hint"
      />
    </template>
  </ComposerBox>
</template>
