<script setup lang="ts">
// An attached image over the whole window (conventions of ImageViewer.vue), and its editor: shapes drawn on a canvas at
// the image's own size, kept apart until Save flattens them into a new file, so that undo is one pop and what was under
// a redaction is in no saved pixel. Closing asks only when something was drawn.
import { attachmentMediaType } from '~/lib/session/attachments'
import { COLORS, drawsSomething, flatten, nextStep, savedName, savedType, STROKES, type Point, type Shape, type ShapeKind, type Tool } from '~/lib/session/annotate'

const props = defineProps<{ file: File, url: string }>()
const emit = defineEmits<{ close: [], save: [file: File] }>()
const toast = useToast()

const SHAPES: { tool: Extract<Tool, ShapeKind | 'step'>, label: string }[] = [
  { tool: 'line', label: tt('Line') },
  { tool: 'arrow', label: tt('Arrow') },
  { tool: 'rect', label: tt('Rectangle') },
  { tool: 'ellipse', label: tt('Ellipse') },
  { tool: 'step', label: tt('Step') },
  { tool: 'redact', label: tt('Redact') }
]

const root = ref<HTMLElement | null>(null)
const stage = ref<HTMLElement | null>(null)
const canvas = ref<HTMLCanvasElement | null>(null)
const image = shallowRef<HTMLImageElement | null>(null)
const stageSize = ref({ width: 0, height: 0 })

const editing = ref(false)
const tool = ref<Tool | null>('pen')
const lastShape = ref<(typeof SHAPES)[number]['tool']>('arrow')
const color = ref(COLORS[0]!.value)
const stroke = ref(1)
const menu = ref<'' | 'shape' | 'color' | 'stroke'>('')
const shapes = shallowRef<Shape[]>([])
const asking = ref(false)
const saving = ref(false)
const dirty = computed(() => shapes.value.length > 0)
const isShape = computed(() => SHAPES.some(s => s.tool === tool.value))

// The image at the most its own size, fitted to the stage; `scale` is image pixels per CSS pixel.
const size = computed(() => {
  const img = image.value
  return img ? { width: img.naturalWidth, height: img.naturalHeight } : { width: 0, height: 0 }
})
const fit = computed(() => {
  const { width, height } = size.value
  if (!width || !height || !stageSize.value.width) return null
  const k = Math.min(1, stageSize.value.width / width, stageSize.value.height / height)
  return { width: Math.round(width * k), height: Math.round(height * k) }
})
const scale = computed(() => fit.value ? size.value.width / fit.value.width : 1)
const strokeWidth = computed(() => Math.max(1, Math.round(STROKES[stroke.value]! * scale.value)))

let observer: ResizeObserver | undefined
onMounted(() => {
  const img = new Image()
  img.onload = () => {
    image.value = img
  }
  img.src = props.url
  if (stage.value) {
    const r = stage.value.getBoundingClientRect()
    stageSize.value = { width: r.width, height: r.height }
    observer = new ResizeObserver(([e]) => {
      stageSize.value = { width: e!.contentRect.width, height: e!.contentRect.height }
    })
    observer.observe(stage.value)
  }
  window.addEventListener('keydown', onKey, true)
  root.value?.focus()
})
onBeforeUnmount(() => {
  observer?.disconnect()
  window.removeEventListener('keydown', onKey, true)
})

// ── drawing ──────────────────────────────────────────────────────────────

let draft: Shape | null = null
function repaint() {
  const ctx = canvas.value?.getContext('2d')
  if (!ctx || !image.value) return
  flatten(ctx, image.value, size.value, draft ? [...shapes.value, draft] : shapes.value)
}
watch(shapes, repaint)
// The canvas appears empty, whenever it appears.
watch(canvas, repaint, { flush: 'post' })

function pointOf(e: PointerEvent): Point {
  const r = canvas.value!.getBoundingClientRect()
  return { x: (e.clientX - r.left) * size.value.width / r.width, y: (e.clientY - r.top) * size.value.height / r.height }
}

function onDown(e: PointerEvent) {
  if (e.button !== 0) return
  if (textBox.value) return commitText()
  menu.value = ''
  const at = pointOf(e)
  if (tool.value === 'text') return openText(at)
  if (tool.value === 'step') {
    shapes.value = [...shapes.value, { kind: 'step', color: color.value, at, n: nextStep(shapes.value), radius: (10 + STROKES[stroke.value]!) * scale.value }]
    return
  }
  if (!tool.value) return
  canvas.value!.setPointerCapture(e.pointerId)
  draft = tool.value === 'pen'
    ? { kind: 'pen', color: color.value, width: strokeWidth.value, points: [at] }
    : { kind: tool.value, color: color.value, width: strokeWidth.value, from: at, to: at }
  repaint()
}
function onMove(e: PointerEvent) {
  if (!draft) return
  if (draft.kind === 'pen') draft.points.push(pointOf(e))
  else if ('to' in draft) draft.to = pointOf(e)
  repaint()
}
function onUp() {
  const done = draft
  draft = null
  if (done && drawsSomething(done)) shapes.value = [...shapes.value, done]
  else repaint()
}

function undo() {
  shapes.value = shapes.value.slice(0, -1)
}

// ── text ─────────────────────────────────────────────────────────────────

const textBox = ref<{ at: Point, value: string } | null>(null)
const textInput = ref<HTMLTextAreaElement | null>(null)
const textSize = computed(() => (12 + STROKES[stroke.value]! * 2) * scale.value)
// The half of the extra line height above a line: the box's text and the painted one sit at the same place.
const LEADING = 0.125
function openText(at: Point) {
  textBox.value = { at, value: '' }
  nextTick(() => textInput.value?.focus())
}
function commitText() {
  const box = textBox.value
  textBox.value = null
  if (!box || !box.value.trim()) return
  const family = root.value ? getComputedStyle(root.value).fontFamily : 'monospace'
  const size = textSize.value
  shapes.value = [...shapes.value, { kind: 'text', color: color.value, at: { x: box.at.x, y: box.at.y + size * LEADING }, text: box.value, size, font: `${size}px ${family}` }]
}
function onTextKey(e: KeyboardEvent) {
  if (e.key !== 'Enter' || e.shiftKey || e.isComposing) return
  e.preventDefault()
  commitText()
}
const textRows = computed(() => (textBox.value?.value ?? '').split('\n'))

// ── leaving ──────────────────────────────────────────────────────────────

function requestClose() {
  commitText()
  if (dirty.value) asking.value = true
  else emit('close')
}

async function save() {
  commitText()
  const el = canvas.value
  if (!el || !dirty.value || saving.value) return
  saving.value = true
  draft = null
  repaint()
  const blob = await new Promise<Blob | null>(resolve => el.toBlob(resolve, savedType(attachmentMediaType(props.file)), 0.92))
  saving.value = false
  if (!blob) {
    toast.add({ title: tt('Could not save the image'), description: tt('It is too large for this window to draw.'), color: 'error' })
    return
  }
  emit('save', new File([blob], savedName(props.file.name, blob.type), { type: blob.type }))
}

function onKey(e: KeyboardEvent) {
  const stop = () => {
    e.preventDefault()
    e.stopPropagation()
  }
  if (e.key === 'Escape') {
    if (asking.value) asking.value = false
    else if (textBox.value) textBox.value = null
    else if (menu.value) menu.value = ''
    else requestClose()
    stop()
  } else if (editing.value && !asking.value && !textBox.value && (e.metaKey || e.ctrlKey) && !e.shiftKey && e.key.toLowerCase() === 'z') {
    undo()
    stop()
  }
}

const TOOL_BTN = 'flex size-[30px] shrink-0 items-center justify-center hover:text-highlighted'
const toolLook = (on: boolean) => on ? 'bg-raised text-strong' : 'text-muted'
</script>

<template>
  <Teleport to="body">
    <div
      ref="root"
      role="dialog"
      aria-modal="true"
      :aria-label="file.name"
      tabindex="-1"
      class="fixed inset-0 z-50 flex flex-col bg-black/85 outline-none"
      @click.self="!editing && emit('close')"
    >
      <div
        class="flex min-h-0 flex-1 p-10"
        @click.self="!editing && emit('close')"
      >
        <div
          ref="stage"
          class="flex size-full min-h-0 min-w-0 items-center justify-center"
          @click.self="!editing && emit('close')"
        >
          <div
            v-if="fit"
            class="relative shrink-0"
            :style="{ width: `${fit.width}px`, height: `${fit.height}px` }"
          >
            <canvas
              v-if="editing"
              ref="canvas"
              :width="size.width"
              :height="size.height"
              class="block size-full touch-none"
              :class="tool ? (tool === 'text' ? 'cursor-text' : 'cursor-crosshair') : ''"
              @mousedown.prevent
              @pointerdown="onDown"
              @pointermove="onMove"
              @pointerup="onUp"
              @pointercancel="onUp"
            />
            <img
              v-else
              :src="url"
              :alt="file.name"
              class="block size-full"
            >
            <textarea
              v-if="textBox"
              ref="textInput"
              v-model="textBox.value"
              :aria-label="tt('Text on the image')"
              :rows="textRows.length"
              :cols="Math.max(8, ...textRows.map(l => l.length + 1))"
              class="absolute resize-none overflow-hidden bg-transparent p-0 leading-[1.25] whitespace-pre outline-1 outline-white/60 outline-dashed"
              :style="{ left: `${textBox.at.x / scale}px`, top: `${textBox.at.y / scale}px`, fontSize: `${textSize / scale}px`, color }"
              @keydown="onTextKey"
              @blur="commitText"
            />
          </div>
        </div>
      </div>

      <div
        v-if="editing"
        role="toolbar"
        :aria-label="tt('Edit the image')"
        class="flex shrink-0 justify-center px-4 pb-3"
      >
        <div class="flex items-center gap-1 border border-accented bg-surface p-1">
          <HIconBtn
            icon="edit"
            :label="tt('Pencil')"
            :size="30"
            :pressed="tool === 'pen'"
            @click="tool = tool === 'pen' ? null : 'pen'"
          />

          <UPopover
            :open="menu === 'shape'"
            :portal="false"
            :content="{ side: 'top', align: 'center', sideOffset: 8 }"
            @update:open="menu = $event ? 'shape' : ''"
          >
            <button
              type="button"
              :aria-label="tt('Shapes')"
              :title="tt('Shapes')"
              :class="[TOOL_BTN, toolLook(isShape)]"
            >
              <AnnotateGlyph :tool="isShape ? tool as typeof lastShape : lastShape" />
            </button>
            <template #content>
              <div
                role="menu"
                :aria-label="tt('Shapes')"
                class="w-[160px] border border-accented bg-hover py-1 text-[12.5px]"
              >
                <PaneMenuItem
                  v-for="s in SHAPES"
                  :key="s.tool"
                  role="menuitemradio"
                  :checked="tool === s.tool"
                  reserve
                  @click="tool = s.tool; lastShape = s.tool; menu = ''"
                >
                  <AnnotateGlyph :tool="s.tool" />{{ s.label }}
                </PaneMenuItem>
              </div>
            </template>
          </UPopover>

          <button
            type="button"
            :aria-label="tt('Text')"
            :title="tt('Text')"
            :aria-pressed="tool === 'text'"
            :class="[TOOL_BTN, toolLook(tool === 'text')]"
            @click="tool = 'text'"
          >
            <AnnotateGlyph tool="text" />
          </button>

          <UPopover
            :open="menu === 'color'"
            :portal="false"
            :content="{ side: 'top', align: 'center', sideOffset: 8 }"
            @update:open="menu = $event ? 'color' : ''"
          >
            <button
              type="button"
              :aria-label="tt('Colour')"
              :title="tt('Colour')"
              :class="[TOOL_BTN, 'text-muted']"
            >
              <span
                class="size-4 rounded-chip outline-1 outline-line-strong"
                :style="{ background: color }"
              />
            </button>
            <template #content>
              <div
                role="menu"
                :aria-label="tt('Colour')"
                class="w-[140px] border border-accented bg-hover py-1 text-[12.5px]"
              >
                <PaneMenuItem
                  v-for="c in COLORS"
                  :key="c.value"
                  role="menuitemradio"
                  :checked="color === c.value"
                  reserve
                  @click="color = c.value; menu = ''"
                >
                  <span
                    class="size-3.5 rounded-chip outline-1 outline-line-strong"
                    :style="{ background: c.value }"
                  />{{ c.name }}
                </PaneMenuItem>
              </div>
            </template>
          </UPopover>

          <UPopover
            :open="menu === 'stroke'"
            :portal="false"
            :content="{ side: 'top', align: 'center', sideOffset: 8 }"
            @update:open="menu = $event ? 'stroke' : ''"
          >
            <button
              type="button"
              :aria-label="tt('Thickness')"
              :title="tt('Thickness')"
              :class="[TOOL_BTN, 'text-muted']"
            >
              <span
                class="block w-4 rounded-chip bg-current"
                :style="{ height: `${Math.min(STROKES[stroke]!, 10)}px` }"
              />
            </button>
            <template #content>
              <div
                role="menu"
                :aria-label="tt('Thickness')"
                class="w-[120px] border border-accented bg-hover py-1 text-[12.5px]"
              >
                <PaneMenuItem
                  v-for="(w, i) in STROKES"
                  :key="w"
                  role="menuitemradio"
                  :aria-label="`${w} pixels`"
                  :checked="stroke === i"
                  reserve
                  @click="stroke = i; menu = ''"
                >
                  <span
                    class="block w-12 rounded-chip bg-current"
                    :style="{ height: `${w}px` }"
                  />
                </PaneMenuItem>
              </div>
            </template>
          </UPopover>

          <span class="mx-1 h-4 w-px bg-line" />
          <HBtn
            variant="primary"
            size="md"
            :disabled="!dirty || saving"
            @click="save"
          >
            {{ tt('Save') }}
          </HBtn>
          <HBtn
            size="md"
            @click="requestClose"
          >
            {{ tt('Close') }}
          </HBtn>
        </div>
      </div>

      <HIconBtn
        icon="close"
        :label="tt('Close')"
        :size="30"
        class="absolute top-3 right-3"
        @click="requestClose"
      />
      <HIconBtn
        v-if="!editing"
        icon="edit"
        :label="tt('Edit')"
        :size="30"
        class="absolute top-[50px] right-3"
        @click="editing = true"
      />

      <div
        v-if="asking"
        class="absolute inset-0 flex items-center justify-center bg-black/50"
      >
        <div
          role="alertdialog"
          :aria-label="tt('Discard your changes?')"
          class="flex w-[320px] flex-col gap-3 border border-accented bg-surface p-4 text-[12.5px]"
        >
          <p class="m-0 text-strong">
            {{ tt('Discard your changes to this image?') }}
          </p>
          <div class="flex justify-end gap-2">
            <HBtn
              size="md"
              @click="asking = false"
            >
              {{ tt('Keep editing') }}
            </HBtn>
            <HBtn
              size="md"
              tone="danger"
              @click="emit('close')"
            >
              {{ tt('Discard') }}
            </HBtn>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>
