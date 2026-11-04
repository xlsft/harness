<script setup lang="ts">
// The message box of both the session composer and the new-session screen (Composer.dc.html): the field, "+" menu,
// attachments, drop target, voice, `/` and `@` menus and the send button. What the text means is the parent's: it owns
// the draft (text, mode, files), gets `submit` and says what the screen has. `busy` is a running turn (queue and stop),
// `sending` a send under way; the #top, #instead (in place of the field) and #bottom slots sit in the same column.
import type { ComposerFile, ComposerMode, ComposerPreset, MenuOption } from '~/utils/composer'
import { isDesktop, pickFolder } from '~/lib/desktop'
import { clipboardFiles } from '~/lib/session/attachments'

const props = withDefaults(defineProps<{
  commands: MenuOption[]
  // The rows of the @ menu for what follows the @.
  suggest: (query: string) => Promise<MenuOption[]>
  placeholder: string
  // Where "Add folder" starts and what its mentions are relative to; '' for none.
  cwd: string
  busy?: boolean
  sending?: boolean
  planMode?: boolean
  hasShell?: boolean
  hasMemory?: boolean
  error?: boolean
  sendLabel?: string
  // What the slash menu says with no commands.
  noCommands?: string
  preset?: ComposerPreset
}>(), { sendLabel: 'Send', noCommands: undefined, preset: undefined })
const emit = defineEmits<{ submit: [], stop: [], retry: [] }>()
const text = defineModel<string>('text', { required: true })
const mode = defineModel<ComposerMode>('mode', { default: 'normal' })
const files = defineModel<ComposerFile[]>('files', { required: true })
const now = useNow()

// The "+" menu: attach files, mention a folder, start a slash command, or a shell command.
const plusOpen = ref(false)
const browsingFolder = ref(false)
function focusEnd() {
  nextTick(() => {
    const el = input.value
    if (!el) return
    el.focus()
    el.setSelectionRange(el.value.length, el.value.length)
  })
}
defineExpose({ focusEnd })
function insertText(t: string) {
  text.value = text.value && !/\s$/.test(text.value) ? `${text.value} ${t}` : `${text.value}${t}`
  focusEnd()
}
function mentionFolder(path: string | null) {
  browsingFolder.value = false
  if (path) insertText(folderMention(path, props.cwd))
}
async function plus(action: 'files' | 'folder' | 'slash' | 'shell') {
  plusOpen.value = false
  if (action === 'files') fileInput.value?.click()
  else if (action === 'folder') {
    if (isDesktop()) mentionFolder(await pickFolder(props.cwd))
    else browsingFolder.value = true
  } else if (action === 'slash') {
    mode.value = 'normal'
    text.value = '/'
    focusEnd()
  } else {
    mode.value = 'shell'
    focusEnd()
  }
}
defineShortcuts({ meta_u: () => previewing.value || plus('files') })
const focused = ref(!!props.preset?.focused)
const dragging = ref(props.preset?.drop ?? 0)
const recording = ref(!!props.preset?.recording)
const recordStart = ref(Date.now())
const menuIndex = ref(0)
const mentionOptions = ref<MenuOption[]>([])
const input = ref<HTMLTextAreaElement | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

const multiline = computed(() => text.value.includes('\n'))
const hasText = computed(() => text.value.trim().length > 0)
// A message of files alone goes too; shell and memory are text.
const hasContent = computed(() => hasText.value || (mode.value === 'normal' && files.value.length > 0))
const uploading = computed(() => files.value.filter(f => f.progress !== undefined && f.progress < 100).length)
// A file that cannot go holds the message back rather than leaving it out.
const rejected = computed(() => files.value.some(f => f.error))

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
const slashOptions = computed(() => props.commands.filter(c => c.label.startsWith(token.value || '/')).slice(0, 4))
const options = computed(() => menu.value === 'slash' ? slashOptions.value : mentionOptions.value)

let asked = 0
watch([menu, token, () => props.suggest], async () => {
  menuIndex.value = 0
  if (menu.value !== 'mention') return
  const n = ++asked
  const found = await props.suggest(token.value.slice(1))
  if (n === asked) mentionOptions.value = found
}, { immediate: true })

const fieldPlaceholder = computed(() => {
  if (dictation.state.value === 'transcribing') return 'Transcribing…'
  if (mode.value === 'shell') return 'Run a command'
  if (mode.value === 'memory') return 'Something to remember'
  return props.placeholder
})

const frame = computed(() => {
  if (props.error) return 'border-bad'
  if (mode.value === 'shell') return 'border-orange'
  if (mode.value === 'memory') return 'border-pink'
  if (recording.value) return 'border-bad'
  if (props.planMode && !props.busy) return 'border-cyan'
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

function send() {
  if (!rejected.value) emit('submit')
}

function pick(o: MenuOption) {
  forcedMenu.value = ''
  if (o.run) return o.run()
  if (menu.value === 'slash') text.value = o.label + ' '
  else text.value = text.value.replace(/@[^\s]*$/, `@${o.key.startsWith('agent:') || o.key.startsWith('mcp:') ? o.label : o.key} `)
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
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    send()
    return
  }
  if (e.key === 'Backspace' && !text.value && mode.value !== 'normal') {
    mode.value = 'normal'
    e.preventDefault()
  }
}

// Quote lines of the text ("> …"), drawn under the field.
const quoteLines = computed(() => text.value.split('\n').map((l) => {
  const mark = l.match(/^>\s?/)?.[0] ?? ''
  return { text: l, quote: !!mark, mark }
}))
const hasQuotes = computed(() => mode.value === 'normal' && quoteLines.value.some(l => l.quote))
const mirror = ref<HTMLElement | null>(null)
function syncMirror() {
  if (mirror.value && input.value) mirror.value.scrollTop = input.value.scrollTop
}

function onInput(e: Event) {
  const v = (e.target as HTMLTextAreaElement).value
  // A leading $ (or !) switches to a shell command, # to memory.
  if (mode.value === 'normal' && ((props.hasMemory && v === '#') || (props.hasShell && (v === '$' || v === '!')))) {
    mode.value = v === '#' ? 'memory' : 'shell'
    text.value = ''
    return
  }
  text.value = v
}

function addFiles(list: ArrayLike<File> | null | undefined) {
  if (!list) return
  files.value = [...files.value, ...Array.from(list, composerFile)]
}

function onPicked(e: Event) {
  const el = e.target as HTMLInputElement
  addFiles(el.files)
  // The same file can be picked again after removing it.
  el.value = ''
}

// A paste with files attaches them; everything else stays the field's own paste.
function onPaste(e: ClipboardEvent) {
  if (mode.value !== 'normal' || !e.clipboardData) return
  const pasted = clipboardFiles(e.clipboardData)
  if (!pasted.length) return
  e.preventDefault()
  addFiles(pasted)
}

// The thumbnails: one object URL per attached file, for as long as it is attached.
const thumbs = shallowRef(new Map<File, string>())
watch(files, (now) => {
  const held = new Set(now.flatMap(f => f.file ? [f.file] : []))
  const next = new Map<File, string>()
  for (const [f, url] of thumbs.value) {
    if (held.has(f)) next.set(f, url)
    else URL.revokeObjectURL(url)
  }
  for (const f of held) {
    if (!next.has(f)) next.set(f, URL.createObjectURL(f))
  }
  thumbs.value = next
}, { immediate: true })
onBeforeUnmount(() => thumbs.value.forEach(url => URL.revokeObjectURL(url)))

// The image open over the window; saving its edit replaces the file.
const previewing = shallowRef<ComposerFile | null>(null)
function saveEdit(file: File) {
  const open = previewing.value
  previewing.value = null
  if (open) files.value = files.value.map(f => f === open ? composerFile(file) : f)
  focusEnd()
}
function closePreview() {
  previewing.value = null
  focusEnd()
}

function onDrop(e: DragEvent) {
  dragging.value = 0
  addFiles(e.dataTransfer?.files)
}

const wave = [4, 8, 14, 10, 18, 22, 16, 9, 6, 12, 20, 24, 18, 11, 7, 5, 9, 15, 21, 17, 12, 8, 6, 10, 16, 22, 19, 13, 8, 5, 7, 12, 18, 23, 20, 14, 9, 6, 4, 8, 13, 19, 24, 18, 12, 7, 5, 9, 14, 20, 16, 10, 6, 4, 7, 11, 15, 10, 6, 4]
function mmss(ms: number) {
  const sec = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`
}
// Dictation (useDictation): the microphone, then the speech-to-text provider of Settings › Speech.
const dictation = useDictation()
const toast = useToast()
async function startRecording() {
  try {
    await dictation.start()
    recording.value = true
    recordStart.value = dictation.startedAt.value
  } catch (e) {
    toast.add({ title: 'Dictation', description: e instanceof Error ? e.message : String(e), color: 'error' })
  }
}
// keep: insert the text; send: and send it.
async function stopRecording(keep: boolean, thenSend = false) {
  recording.value = false
  try {
    const said = await dictation.stop(keep)
    if (said) text.value = (text.value ? text.value.replace(/\s*$/, ' ') : '') + said
    if (said && thenSend) send()
  } catch (e) {
    toast.add({ title: 'Could not transcribe', description: e instanceof Error ? e.message : String(e), color: 'error' })
  }
}

const ICON_BTN = 'flex size-7 shrink-0 items-center justify-center'
</script>

<template>
  <div
    class="flex flex-col gap-1.5"
    @dragenter.prevent="dragging = Math.max(1, dragging)"
    @dragover.prevent
  >
    <slot name="top" />

    <!-- menus -->
    <ComposerMenus
      v-if="menu"
      :kind="menu"
      :options="options"
      :active="menuIndex"
      :empty="noCommands"
      @pick="pick"
      @hover="menuIndex = $event"
    />

    <!-- in place of the field: offline, ended -->
    <slot
      v-if="$slots.instead"
      name="instead"
    />

    <!-- drop target -->
    <div
      v-else-if="dragging"
      class="flex min-h-[88px] flex-col items-center justify-center gap-1.5 border border-dashed border-accent bg-accent/8 text-[12.5px] text-strong"
      @dragleave.prevent="dragging = 0"
      @drop.prevent="onDrop"
    >
      <HIcon
        name="upload"
        :size="20"
        class="text-accent"
      />
      Drop {{ dragging > 1 ? `${dragging} files` : 'files' }} to attach<span class="text-[11px] text-muted">images, PDFs and text files</span>
    </div>

    <!-- voice recording -->
    <div
      v-else-if="recording"
      class="flex min-h-11 items-center gap-2.5 border border-bad bg-surface px-2"
    >
      <span class="size-2 bg-bad" /><span class="text-[12px] text-strong">{{ mmss(now - recordStart) }}</span>
      <div class="flex h-6 min-w-0 flex-1 items-center justify-end gap-0.5 overflow-hidden">
        <span
          v-for="(h, i) in (dictation.state.value === 'recording' ? dictation.levels.value.map(l => 2 + l * 22) : wave)"
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
        aria-label="Insert the text"
        title="Insert the text without sending"
        :class="ICON_BTN"
        class="border border-default text-strong"
        @click="stopRecording(true)"
      >
        <HIcon
          name="check"
          :size="14"
        />
      </button>
      <button
        type="button"
        aria-label="Send"
        title="Send what you said"
        :class="ICON_BTN"
        class="bg-accent text-on-accent"
        @click="stopRecording(true, true)"
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
        <AttachmentThumb
          v-for="(f, i) in files"
          :key="i"
          :file="f"
          :url="f.file ? thumbs.get(f.file) : undefined"
          @open="previewing = f"
          @remove="files = files.filter((_, at) => at !== i)"
        />
      </div>
      <div
        class="flex gap-1.5 px-2"
        :class="multiline ? 'items-end py-2' : 'min-h-[42px] items-center'"
        :style="!files.length && !multiline ? { minHeight: '42px' } : undefined"
      >
        <span
          v-if="mode === 'shell'"
          title="Shell command"
          aria-label="Shell command"
          class="flex items-center border border-orange px-[3px] py-px text-[11px] text-orange"
        ><HIcon
          name="term"
          :size="12"
        /></span>
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
        <UPopover
          v-else
          v-model:open="plusOpen"
          :content="{ side: 'top', align: 'start', sideOffset: 6 }"
        >
          <button
            type="button"
            aria-label="Add"
            :class="ICON_BTN"
            class="text-muted hover:text-highlighted"
          >
            <HIcon name="plus" />
          </button>
          <template #content>
            <div
              role="menu"
              aria-label="Add"
              class="w-[230px] border border-accented bg-hover py-1 text-[12.5px]"
            >
              <PaneMenuItem
                icon="attach"
                kbd="meta+u"
                @click="plus('files')"
              >
                Add files or photos
              </PaneMenuItem>
              <PaneMenuItem
                icon="folder"
                @click="plus('folder')"
              >
                Add folder
              </PaneMenuItem>
              <PaneMenuItem
                icon="slash"
                @click="plus('slash')"
              >
                Slash commands
              </PaneMenuItem>
              <PaneMenuItem
                v-if="hasShell"
                icon="term"
                @click="plus('shell')"
              >
                Execute shell
              </PaneMenuItem>
            </div>
          </template>
        </UPopover>
        <input
          ref="fileInput"
          type="file"
          multiple
          class="hidden"
          @change="onPicked"
        >
        <div class="relative grid min-w-0 flex-1">
          <!-- What the field shows when it has quotes: quote lines dimmed with a rule, under the transparent text -->
          <div
            v-if="hasQuotes"
            ref="mirror"
            aria-hidden="true"
            class="pointer-events-none col-start-1 row-start-1 max-h-[167px] overflow-hidden py-[3px] leading-[1.6] break-words whitespace-pre-wrap text-strong"
          >
            <span
              v-for="(l, i) in quoteLines"
              :key="i"
              :class="l.quote && 'text-muted shadow-[inset_2px_0_0_var(--color-accent)]'"
            ><span
              v-if="l.mark"
              class="text-transparent"
            >{{ l.mark }}</span>{{ l.text.slice(l.mark.length) + (i < quoteLines.length - 1 ? '\n' : '\u200b') }}</span>
          </div>
          <textarea
            ref="input"
            :value="text"
            rows="1"
            :placeholder="fieldPlaceholder"
            aria-label="Message"
            class="col-start-1 row-start-1 min-w-0 resize-none bg-transparent py-[3px] leading-[1.6] outline-none placeholder:truncate placeholder:text-muted"
            :class="[hasQuotes ? 'text-transparent caret-(--color-strong)' : menu === 'slash' ? 'text-accent' : 'text-strong', !text && 'overflow-hidden']"
            @input="onInput"
            @keydown="onKeydown"
            @paste="onPaste"
            @focus="focused = true"
            @blur="focused = false"
            @scroll="syncMirror"
          />
        </div>
        <template v-if="error">
          <button
            type="button"
            class="flex items-center gap-1.5 border border-bad px-2.5 py-1 text-[12px] text-strong"
            @click="emit('retry')"
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
            v-if="busy && hasContent"
            type="button"
            aria-label="Queue"
            :class="ICON_BTN"
            class="bg-accent text-on-accent"
            @click="send"
          >
            <HIcon name="up" />
          </button>
          <button
            v-if="busy"
            type="button"
            aria-label="Stop"
            title="Stop this turn. Agents keep running: stop them in the Agents pane"
            :class="ICON_BTN"
            class="bg-warn text-black"
            @click="emit('stop')"
          >
            <HIcon
              name="stop"
              :size="13"
            />
          </button>
          <button
            v-else
            type="button"
            :aria-label="mode === 'shell' ? 'Run' : sendLabel"
            :disabled="!hasContent || uploading > 0 || rejected || sending"
            :class="[ICON_BTN, !hasContent || uploading || rejected ? 'bg-raised text-disabled' : mode === 'shell' ? 'bg-orange text-black' : mode === 'memory' ? 'bg-pink text-on-accent' : 'bg-accent text-on-accent']"
            @click="send"
          >
            <HIcon
              :name="sending ? 'loading' : 'up'"
              :spin="sending"
            />
          </button>
        </template>
      </div>
    </div>

    <slot name="bottom" />

    <UModal
      v-model:open="browsingFolder"
      :ui="{ content: 'w-auto max-w-none bg-transparent ring-0' }"
    >
      <template #content>
        <NewDirectoryBrowser
          :start="cwd"
          @pick="mentionFolder"
          @close="browsingFolder = false"
        />
      </template>
    </UModal>

    <AttachmentPreview
      v-if="previewing?.file"
      :file="previewing.file"
      :url="thumbs.get(previewing.file) ?? ''"
      @close="closePreview"
      @save="saveEdit"
    />
  </div>
</template>
