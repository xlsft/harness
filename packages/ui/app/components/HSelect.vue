<script setup lang="ts">
// Square select: a button showing the chosen option and a listbox panel under it (no native <select>, so it
// matches the rest of the UI). Options may carry a hint (dim, after the label) and a group (a heading above
// consecutive options of that group). Keyboard: arrows move, Enter / Space pick, Esc closes.
export interface HSelectOption {
  value: string
  label: string
  hint?: string
  group?: string
}

const props = defineProps<{
  modelValue: string
  options: HSelectOption[]
  placeholder?: string
  ariaLabel?: string
  disabled?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const open = ref(false)
const active = ref(-1)
const root = ref<HTMLElement | null>(null)
const list = ref<HTMLElement | null>(null)
const id = useId()

const selected = computed(() => props.options.find(o => o.value === props.modelValue))

// A group heading goes before the first option of each run of the same group.
const rows = computed(() => props.options.map((o, i) => ({ ...o, index: i, heading: o.group && o.group !== props.options[i - 1]?.group ? o.group : '' })))

function show() {
  if (props.disabled || open.value) return
  open.value = true
  active.value = Math.max(0, props.options.findIndex(o => o.value === props.modelValue))
  nextTick(scrollActive)
}

function close() {
  open.value = false
}

function pick(i: number) {
  const o = props.options[i]
  if (!o) return
  if (o.value !== props.modelValue) emit('update:modelValue', o.value)
  close()
}

function scrollActive() {
  list.value?.querySelector<HTMLElement>(`[data-index="${active.value}"]`)?.scrollIntoView({ block: 'nearest' })
}

function move(d: number) {
  const n = props.options.length
  if (!n) return
  active.value = (active.value + d + n) % n
  nextTick(scrollActive)
}

function onKey(e: KeyboardEvent) {
  if (props.disabled) return
  switch (e.key) {
    case 'ArrowDown':
    case 'ArrowUp':
      e.preventDefault()
      if (!open.value) show()
      else move(e.key === 'ArrowDown' ? 1 : -1)
      break
    case 'Home':
    case 'End':
      if (!open.value) return
      e.preventDefault()
      active.value = e.key === 'Home' ? 0 : props.options.length - 1
      nextTick(scrollActive)
      break
    case 'Enter':
    case ' ':
      e.preventDefault()
      if (open.value) pick(active.value)
      else show()
      break
    case 'Escape':
      if (open.value) {
        e.preventDefault()
        e.stopPropagation()
        close()
      }
      break
    case 'Tab':
      close()
      break
  }
}

function onPointer(e: PointerEvent) {
  if (!root.value?.contains(e.target as Node)) close()
}

watch(open, (v) => {
  if (v) document.addEventListener('pointerdown', onPointer, true)
  else document.removeEventListener('pointerdown', onPointer, true)
})

onBeforeUnmount(() => document.removeEventListener('pointerdown', onPointer, true))
</script>

<template>
  <div
    ref="root"
    class="relative w-[300px] shrink-0 max-sm:w-full"
  >
    <button
      type="button"
      role="combobox"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="`${id}-list`"
      :aria-activedescendant="open && active >= 0 ? `${id}-${active}` : undefined"
      :aria-label="ariaLabel"
      :disabled="disabled"
      class="flex h-8 w-full items-center gap-2 border border-accented bg-default px-2 text-left text-[12.5px] outline-none focus-visible:border-accent disabled:opacity-50"
      :class="open ? 'border-accent' : ''"
      @click="open ? close() : show()"
      @keydown="onKey"
    >
      <span
        class="min-w-0 flex-1 truncate"
        :class="selected ? 'text-white' : 'text-muted'"
      >{{ selected?.label ?? placeholder ?? '' }}</span>
      <span
        v-if="selected?.hint"
        class="shrink-0 truncate text-[11.5px] text-muted"
      >{{ selected.hint }}</span>
      <HIcon
        name="down"
        :size="12"
        class="text-muted"
      />
    </button>
    <div
      v-if="open"
      :id="`${id}-list`"
      ref="list"
      role="listbox"
      :aria-label="ariaLabel"
      class="menu-panel absolute top-full right-0 left-0 z-30 mt-1 max-h-[280px] overflow-y-auto"
    >
      <template
        v-for="o in rows"
        :key="o.index"
      >
        <div
          v-if="o.heading"
          role="presentation"
          class="px-2 pt-2 pb-1 text-[11px] text-muted"
        >
          {{ o.heading }}
        </div>
        <div
          :id="`${id}-${o.index}`"
          role="option"
          :data-index="o.index"
          :aria-selected="o.value === modelValue"
          class="flex cursor-pointer items-center gap-2 px-2 py-1.5"
          :class="o.index === active ? 'bg-line text-white' : ''"
          @pointerenter="active = o.index"
          @click="pick(o.index)"
        >
          <span class="min-w-0 flex-1 truncate">{{ o.label }}</span>
          <span
            v-if="o.hint"
            class="shrink-0 text-[11.5px] text-muted"
          >{{ o.hint }}</span>
          <HIcon
            v-if="o.value === modelValue"
            name="check"
            :size="12"
            class="text-accent"
          />
        </div>
      </template>
      <div
        v-if="!options.length"
        class="px-2 py-1.5 text-muted"
      >
        No options
      </div>
    </div>
  </div>
</template>
