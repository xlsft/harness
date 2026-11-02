<script setup lang="ts">
// Composer (Composer.dc.html): Enter sends, Shift+Enter new line, Cmd/Ctrl+Enter queues; while a turn runs, messages queue (✕ removes one)
// and Stop interrupts. `/` and `@` open menus, `!` runs a shell command, `#` saves to memory. Status line, queue,
// attachments, drop, voice, context warning, permission waiting, offline and ended states.
import type { Hint } from '~/components/composer/ComposerFooter.vue'
import type { SessionState } from '~/lib/session/types'
import type { ComposerPreset, MenuOption } from '~/utils/composer'

const props = defineProps<{ session: SessionState, preset?: ComposerPreset }>()
const store = useSessionStore()
const now = useNow()

const text = ref(props.preset?.text ?? '')
const mode = ref<'normal' | 'shell' | 'memory'>('normal')
const focused = ref(!!props.preset?.focused)
const error = ref(props.preset?.error ?? '')
const failedText = ref('')
const files = ref(props.preset?.files ?? [])
const dragging = ref(props.preset?.drop ?? 0)
const recording = ref(!!props.preset?.recording)
const recordStart = ref(Date.now())
const menuIndex = ref(0)
const mentionOptions = ref<MenuOption[]>([])
const memoryTarget = ref('CLAUDE.md')
const input = ref<HTMLTextAreaElement | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

const id = computed(() => props.session.meta.id)
const status = computed(() => props.session.meta.status)
const busy = computed(() => status.value === 'working' || status.value === 'needs_input')
const waiting = computed(() => status.value === 'needs_input')
const offline = computed(() => status.value === 'waiting_for_connection')
const ended = computed(() => status.value === 'ended')
const planMode = computed(() => props.session.meta.permissionMode === 'plan')
const queue = computed(() => store.localQueue[id.value] ?? [])
const multiline = computed(() => text.value.includes('\n'))
const hasText = computed(() => text.value.trim().length > 0)
const uploading = computed(() => files.value.filter(f => f.progress !== undefined && f.progress < 100).length)

const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
const contextPct = computed(() => Math.round((props.session.contextTokens / (model.value?.contextWindow || 200_000)) * 100))

// The token being typed at the cursor decides which menu is open.
const token = computed(() => {
  const m = text.value.match(/(^|\s)([/@][^\s]*)$/)
  return m ? m[2]! : ''
})
const forcedMenu = ref(props.preset?.menu === 'slash' || props.preset?.menu === 'mention' ? props.preset.menu : '')
const menu = computed<'slash' | 'mention' | ''>(() => {
  if (mode.value !== 'normal') return ''
  if (forcedMenu.value) return forcedMenu.value as 'slash' | 'mention'
  if (token.value.startsWith('/') && text.value.trimStart() === token.value) return 'slash'
  if (token.value.startsWith('@')) return 'mention'
  return ''
})
const slashOptions = computed(() => SLASH_COMMANDS.filter(c => c.label.startsWith(token.value || '/')).slice(0, 4))
const options = computed(() => menu.value === 'slash' ? slashOptions.value : mentionOptions.value)

watch([menu, token], async () => {
  menuIndex.value = 0
  if (menu.value !== 'mention') return
  const q = token.value.slice(1)
  const paths = await store.suggestFiles(id.value, q || 'trans')
  mentionOptions.value = [
    ...paths.slice(0, 2).map(p => ({
      key: p,
      label: basename(p) + (p.endsWith('/') ? '/' : ''),
      detail: p.replace(/\/?[^/]+\/?$/, ''),
      icon: p.endsWith('/') ? 'folder' as const : 'file' as const,
      iconCls: p.endsWith('/') ? 'text-muted' : 'text-white'
    })),
    ...MENTION_EXTRAS.filter(e => !q || e.label.includes(q))
  ]
}, { immediate: true })

const activity = computed(() => {
  const s = props.session
  const todo = s.todos.find(t => t.status === 'in_progress')
  if (todo?.activeForm) return todo.activeForm
  const tool = [...s.items].reverse().find(i => i.kind === 'tool' && i.status === 'running' && i.name !== 'Task')
  if (tool && tool.kind === 'tool') return `Running ${tool.name}`
  const last = s.items[s.items.length - 1]
  if (last?.kind === 'assistant' && last.streaming) return last.blocks[last.blocks.length - 1]?.type === 'thinking' ? 'Thinking' : 'Writing'
  return 'Thinking'
})
const pending = computed(() => props.session.items.find(i => i.kind === 'permission' && i.state === 'pending'))

const placeholder = computed(() => {
  if (mode.value === 'shell') return 'Run a command'
  if (mode.value === 'memory') return 'Something to remember'
  if (planMode.value && !busy.value) return 'Describe what to plan. Nothing will be changed.'
  if (queue.value.length) return `${queue.value.length} queued · type to add another`
  if (busy.value) return 'Type to queue a message for after this turn'
  return 'Message harness · / commands · @ files · ! shell'
})

// Footer hint: text segments, each optionally led by a shortcut rendered with UKbd.
const hint = computed<Hint>(() => {
  if (mode.value === 'shell') return [{ text: 'output goes to context ·' }, { keys: 'backspace', text: 'on empty to exit' }]
  if (uploading.value) return [{ text: `waiting for ${uploading.value} upload${uploading.value > 1 ? 's' : ''}` }]
  if (multiline.value) return [{ keys: 'shift+enter', text: 'new line' }]
  if (planMode.value) return [{ keys: 'shift+tab', text: 'cycle mode' }]
  if (busy.value || queue.value.length) return [{ keys: 'meta+enter', text: 'queues a message' }]
  if (contextPct.value >= 90) return [{ keys: 'enter', text: 'send' }, { keys: 'meta+enter', text: 'queue for later' }]
  return [{ keys: 'enter', text: 'send' }, { keys: 'meta+enter', text: 'queue' }]
})

const frame = computed(() => {
  if (error.value) return 'border-bad'
  if (mode.value === 'shell') return 'border-orange'
  if (mode.value === 'memory') return 'border-pink'
  if (recording.value) return 'border-bad'
  if (planMode.value && !busy.value) return 'border-cyan'
  if (menu.value || (focused.value && hasText.value)) return 'border-accent'
  return 'border-accented'
})

function autosize() {
  const el = input.value
  if (!el) return
  el.style.height = 'auto'
  // Empty: one line (a narrow composer would otherwise grow to fit the wrapped placeholder).
  // Otherwise grows up to 8 lines (8 × 20.8px), then scrolls.
  el.style.height = text.value ? `${Math.min(el.scrollHeight, 167)}px` : ''
}
watch(text, () => nextTick(autosize))
// Re-measure when the composer is resized (window, sidebar or pane splits).
let resizeObserver: ResizeObserver | undefined
onBeforeUnmount(() => resizeObserver?.disconnect())
onMounted(() => {
  if (input.value) {
    resizeObserver = new ResizeObserver(() => autosize())
    resizeObserver.observe(input.value)
  }
  autosize()
  if (props.preset?.focused) input.value?.focus()
})

async function submit() {
  const body = text.value
  if (!body.trim() || ended.value) return
  error.value = ''
  if (mode.value === 'shell') {
    text.value = ''
    mode.value = 'normal'
    await safeSend('!' + body)
    return
  }
  if (mode.value === 'memory') {
    text.value = ''
    mode.value = 'normal'
    await safeSend(`# ${body} (save to ${memoryTarget.value})`)
    return
  }
  if (body.trim() === '/compact' && !busy.value) {
    text.value = ''
    await store.compact(id.value)
    return
  }
  text.value = ''
  files.value = []
  await safeSend(body)
}

async function safeSend(body: string) {
  try {
    await store.send(id.value, body)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
    failedText.value = body
    text.value = body
  }
}

function pick(o: MenuOption) {
  if (menu.value === 'slash') text.value = o.label + ' '
  else text.value = text.value.replace(/@[^\s]*$/, `@${o.key.startsWith('agent:') || o.key.startsWith('mcp:') ? o.label : o.key} `)
  forcedMenu.value = ''
  input.value?.focus()
}

function onKeydown(e: KeyboardEvent) {
  if (menu.value && options.value.length) {
    if (e.key === 'ArrowDown') {
      menuIndex.value = (menuIndex.value + 1) % options.value.length
      e.preventDefault()
      return
    }
    if (e.key === 'ArrowUp') {
      menuIndex.value = (menuIndex.value - 1 + options.value.length) % options.value.length
      e.preventDefault()
      return
    }
    if ((e.key === 'Enter' && !e.shiftKey) || e.key === 'Tab') {
      pick(options.value[menuIndex.value]!)
      e.preventDefault()
      return
    }
  }
  if (e.key === 'Escape' && (menu.value || mode.value !== 'normal')) {
    forcedMenu.value = ''
    if (menu.value) text.value = text.value.replace(/[/@][^\s]*$/, '')
    else mode.value = 'normal'
    e.preventDefault()
    e.stopPropagation()
    return
  }
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit()
    return
  }
  if (e.key === 'Backspace' && !text.value && mode.value !== 'normal') {
    mode.value = 'normal'
    e.preventDefault()
  }
}

function onInput(e: Event) {
  const v = (e.target as HTMLTextAreaElement).value
  if (mode.value === 'normal' && (v === '!' || v === '#')) {
    mode.value = v === '!' ? 'shell' : 'memory'
    text.value = ''
    return
  }
  text.value = v
}

function addFiles(list: FileList | null | undefined) {
  if (!list) return
  for (const f of Array.from(list)) {
    files.value.push({
      name: f.name,
      size: f.size,
      image: f.type.startsWith('image/'),
      error: f.size > 50 * 1024 * 1024 ? `too large · ${Math.round(f.size / 1024 / 1024)} MB` : undefined
    })
  }
}

function onDrop(e: DragEvent) {
  dragging.value = 0
  addFiles(e.dataTransfer?.files)
}

function sizeLabel(n: number) {
  return n >= 1024 * 1024 ? `${(n / 1024 / 1024).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`
}

const wave = [4, 8, 14, 10, 18, 22, 16, 9, 6, 12, 20, 24, 18, 11, 7, 5, 9, 15, 21, 17, 12, 8, 6, 10, 16, 22, 19, 13, 8, 5, 7, 12, 18, 23, 20, 14, 9, 6, 4, 8, 13, 19, 24, 18, 12, 7, 5, 9, 14, 20, 16, 10, 6, 4, 7, 11, 15, 10, 6, 4]
function mmss(ms: number) {
  const sec = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`
}
function startRecording() {
  recording.value = true
  recordStart.value = Date.now()
}
function stopRecording(keep: boolean) {
  recording.value = false
  if (keep) text.value = (text.value ? text.value + ' ' : '') + 'check why the stream hangs on ollama when the model takes more than a minute'
}

const ICON_BTN = 'flex size-7 shrink-0 items-center justify-center'
</script>

<template>
  <div
    class="flex flex-col gap-1.5"
    @dragenter.prevent="dragging = Math.max(1, dragging)"
    @dragover.prevent
  >
    <!-- status line -->
    <div
      v-if="waiting"
      class="flex items-center gap-[9px] text-[12px] text-muted"
    >
      <HIcon
        name="shield"
        :size="14"
        class="text-accent"
      />
      <span class="text-white">Waiting for approval</span>
      <template v-if="pending && pending.kind === 'permission'">
        · Allow {{ pending.toolName }}?
      </template>
      <span class="flex-1" /><span class="flex shrink-0 items-center gap-2.5 max-sm:hidden @max-[560px]:hidden">
        <span class="inline-flex items-center gap-1"><HKeys keys="1" />allow</span>
        <span class="inline-flex items-center gap-1"><HKeys keys="2" />always</span>
        <span class="inline-flex items-center gap-1"><HKeys keys="escape" />deny</span>
      </span>
    </div>
    <div
      v-else-if="busy"
      class="flex items-center gap-[9px] text-[12px] whitespace-nowrap text-muted"
    >
      <HIcon
        name="loading"
        :size="14"
        spin
        class="text-accent"
      />
      <span class="min-w-0 truncate"><span class="text-white">{{ activity }}</span>
        · {{ formatDuration(now - (session.turnStartedAt || now)) }} · {{ formatTokens(session.turnTokens) }} tok</span>
      <span class="flex-1" /><span class="inline-flex shrink-0 items-center gap-1 max-sm:hidden @max-[480px]:hidden"><HKeys keys="escape" />to interrupt</span>
    </div>

    <!-- queued messages -->
    <div
      v-if="queue.length"
      class="border border-dashed border-accented text-[12px]"
    >
      <div
        v-for="(q, i) in queue"
        :key="q.id"
        class="flex items-center gap-[9px] px-2.5 py-[5px]"
        :class="i < queue.length - 1 ? 'border-b border-dashed border-default' : ''"
      >
        <span class="text-muted">{{ i + 1 }}</span>
        <span class="min-w-0 flex-1 truncate text-toned">{{ q.text }}</span>
        <button
          type="button"
          class="text-muted hover:text-highlighted"
          @click="text = q.text; store.removeQueued(id, q.id)"
        >
          edit
        </button>
        <button
          type="button"
          :aria-label="`Remove queued message ${i + 1}`"
          class="text-muted hover:text-highlighted"
          @click="store.removeQueued(id, q.id)"
        >
          <HIcon
            name="close"
            :size="12"
          />
        </button>
      </div>
    </div>

    <!-- context almost full -->
    <div
      v-if="contextPct >= 90 && !busy"
      class="flex items-center gap-[9px] border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12px]"
    >
      <HIcon
        name="warn"
        :size="14"
        class="text-warn"
      />
      <span class="text-white">Context {{ contextPct }}% full</span><span class="text-muted">auto-compact at 95%</span>
      <span class="flex-1" /><HBtn @click="store.compact(id)">
        Compact now
      </HBtn>
    </div>

    <!-- menus -->
    <ComposerMenus
      v-if="menu"
      :kind="menu"
      :options="options"
      :active="menuIndex"
      @pick="pick"
      @hover="menuIndex = $event"
    />

    <!-- offline -->
    <div
      v-if="offline"
      aria-disabled="true"
      class="flex min-h-11 items-center gap-2 border border-default bg-default px-2.5 text-muted"
    >
      <HIcon name="wifi" />
      <span class="flex-1">Reconnecting to {{ session.meta.deviceName || 'the' }} client…</span>
      <HBtn
        size="sm"
        @click="store.open(id)"
      >
        Reconnect now
      </HBtn>
    </div>

    <!-- ended / read-only -->
    <div
      v-else-if="ended"
      class="flex min-h-11 items-center gap-2 border border-default px-2.5 text-muted"
    >
      <HIcon name="lock" />
      <span class="flex-1">Session ended · {{ session.endedReason || 'closed' }}</span>
      <HBtn
        variant="primary"
        size="sm"
      >
        Fork to continue
      </HBtn>
    </div>

    <!-- drop target -->
    <div
      v-else-if="dragging"
      class="flex min-h-[88px] flex-col items-center justify-center gap-1.5 border border-dashed border-accent bg-[#2b7fff14] text-[12.5px] text-white"
      @dragleave.prevent="dragging = 0"
      @drop.prevent="onDrop"
    >
      <HIcon
        name="upload"
        :size="20"
        class="text-accent"
      />
      Drop {{ dragging > 1 ? `${dragging} files` : 'files' }} to attach<span class="text-[11px] text-muted">images, video, audio, code, archives up to 50 MB</span>
    </div>

    <!-- voice recording -->
    <div
      v-else-if="recording"
      class="flex min-h-11 items-center gap-2.5 border border-bad bg-surface px-2"
    >
      <span class="size-2 bg-bad" /><span class="text-[12px] text-white">{{ mmss(now - recordStart) }}</span>
      <div class="flex h-6 min-w-0 flex-1 items-center gap-0.5 overflow-hidden">
        <span
          v-for="(h, i) in wave"
          :key="i"
          class="w-0.5 shrink-0 bg-bad"
          :style="{ height: `${h}px` }"
        />
      </div>
      <button
        type="button"
        aria-label="Cancel recording"
        :class="ICON_BTN"
        class="border border-default text-muted"
        @click="stopRecording(false)"
      >
        <HIcon
          name="close"
          :size="13"
        />
      </button>
      <button
        type="button"
        aria-label="Finish recording"
        :class="ICON_BTN"
        class="bg-accent text-white"
        @click="stopRecording(true)"
      >
        <HIcon name="up" />
      </button>
    </div>

    <!-- input -->
    <div
      v-else
      class="border bg-surface"
      :class="frame"
      @drop.prevent="onDrop"
    >
      <div
        v-if="files.length"
        class="flex flex-wrap gap-1.5 px-2 pt-2"
      >
        <span
          v-for="(f, i) in files"
          :key="i"
          class="flex items-center gap-2 border bg-hover text-[12px]"
          :class="[f.error ? 'border-bad' : 'border-default', f.image ? 'py-1 pr-1.5 pl-1' : 'px-1.5 py-1', f.progress !== undefined && f.progress < 100 ? 'min-w-[150px] flex-col items-stretch gap-[3px]' : '']"
        >
          <template v-if="f.progress !== undefined && f.progress < 100">
            <span class="flex gap-2"><span class="text-white">{{ f.name }}</span><span class="flex-1" /><span class="text-accent">{{ f.progress }}%</span></span>
            <span class="flex h-0.5 bg-line"><span
              class="bg-accent"
              :style="{ width: `${f.progress}%` }"
            /></span>
          </template>
          <template v-else>
            <span
              v-if="f.image"
              role="img"
              aria-label="Image thumbnail"
              class="size-[26px] bg-raised bg-[repeating-linear-gradient(135deg,#2b2b2b_0_1px,transparent_1px_6px)]"
            />
            <HIcon
              v-else-if="!f.error"
              name="file"
              :size="14"
              class="text-white"
            />
            <span class="text-white">{{ f.name }}</span>
            <span
              v-if="f.error"
              class="text-bad"
            >{{ f.error }}</span>
            <span
              v-else-if="!f.image"
              class="text-muted"
            >{{ sizeLabel(f.size) }}</span>
            <button
              type="button"
              :aria-label="`Remove ${f.name}`"
              class="text-muted hover:text-highlighted"
              @click="files.splice(i, 1)"
            ><HIcon
              name="close"
              :size="12"
            /></button>
          </template>
        </span>
      </div>
      <div
        class="flex gap-1.5 px-2"
        :class="multiline ? 'items-end py-2' : 'min-h-[42px] items-center'"
        :style="!files.length && !multiline ? { minHeight: '42px' } : undefined"
      >
        <span
          v-if="mode === 'shell'"
          class="flex items-center gap-1 border border-orange px-[5px] text-[11px] text-orange"
        ><HIcon
          name="term"
          :size="12"
        />shell</span>
        <span
          v-else-if="mode === 'memory'"
          class="flex items-center gap-1 border border-pink px-[5px] text-[11px] text-pink"
        ><HIcon
          name="hash"
          :size="12"
        />memory</span>
        <span
          v-else-if="planMode && !busy"
          class="border border-cyan px-[5px] text-[11px] text-cyan"
        >plan</span>
        <button
          v-else
          type="button"
          aria-label="Attach"
          :class="ICON_BTN"
          class="text-muted hover:text-highlighted"
          @click="fileInput?.click()"
        >
          <HIcon name="plus" />
        </button>
        <input
          ref="fileInput"
          type="file"
          multiple
          class="hidden"
          @change="addFiles(($event.target as HTMLInputElement).files)"
        >
        <textarea
          ref="input"
          :value="text"
          rows="1"
          :placeholder="placeholder"
          aria-label="Message"
          class="min-w-0 flex-1 resize-none bg-transparent py-[3px] leading-[1.6] outline-none placeholder:truncate placeholder:text-muted"
          :class="[menu === 'slash' ? 'text-accent' : 'text-white', !text && 'overflow-hidden']"
          @input="onInput"
          @keydown="onKeydown"
          @focus="focused = true"
          @blur="focused = false"
        />
        <template v-if="error">
          <button
            type="button"
            class="flex items-center gap-1.5 border border-bad px-2.5 py-1 text-[12px] text-white"
            @click="error = ''; safeSend(failedText); text = ''"
          >
            <HIcon
              name="retry"
              :size="13"
            />Retry
          </button>
        </template>
        <template v-else>
          <button
            v-if="!multiline && !busy && mode === 'normal'"
            type="button"
            aria-label="Voice"
            :class="ICON_BTN"
            class="text-muted hover:text-highlighted"
            @click="startRecording"
          >
            <HIcon name="mic" />
          </button>
          <button
            v-if="busy && hasText"
            type="button"
            aria-label="Queue"
            :class="ICON_BTN"
            class="bg-accent text-white"
            @click="submit"
          >
            <HIcon name="up" />
          </button>
          <button
            v-if="busy"
            type="button"
            aria-label="Stop"
            :class="ICON_BTN"
            class="bg-white text-black"
            @click="store.interrupt(id)"
          >
            <HIcon
              name="stop"
              :size="13"
            />
          </button>
          <button
            v-else
            type="button"
            :aria-label="mode === 'shell' ? 'Run' : 'Send'"
            :disabled="!hasText || uploading > 0"
            :class="[ICON_BTN, !hasText || uploading ? 'bg-raised text-disabled' : mode === 'shell' ? 'bg-orange text-black' : mode === 'memory' ? 'bg-pink text-white' : 'bg-accent text-white']"
            @click="submit"
          >
            <HIcon name="up" />
          </button>
        </template>
      </div>
    </div>

    <div
      v-if="error"
      class="text-[11.5px] text-bad"
    >
      {{ error }} Your message wasn't sent.
    </div>
    <div
      v-if="mode === 'memory'"
      class="flex flex-wrap gap-2 text-[11.5px] text-muted"
    >
      <span>save to</span>
      <button
        v-for="t in ['CLAUDE.md', 'user memory', '.claude/rules']"
        :key="t"
        type="button"
        class="border px-1.5"
        :class="memoryTarget === t ? 'border-pink text-white' : 'border-default'"
        @click="memoryTarget = t"
      >
        {{ t }}
      </button>
    </div>

    <ComposerFooter
      :session="session"
      :hint="hint"
      :initial-menu="preset?.menu === 'mode' || preset?.menu === 'model' ? preset.menu : ''"
    />
  </div>
</template>
