<script setup lang="ts">
// Square select in the app's look (instead of the native <select>, which the OS draws rounded): a field-like
// button and a menu of options, optionally grouped, with a filter when the list is long.
export interface HSelectOption {
  value: string
  label: string
  hint?: string
  group?: string
}

const props = withDefaults(defineProps<{
  options: HSelectOption[]
  placeholder?: string
  ariaLabel?: string
  disabled?: boolean
}>(), { placeholder: 'Choose…', ariaLabel: undefined, disabled: false })
const model = defineModel<string>({ default: '' })

const open = ref(false)
const query = ref('')
const FILTER_FROM = 9

const current = computed(() => props.options.find(o => o.value === model.value))
const groups = computed(() => {
  const q = query.value.trim().toLowerCase()
  const shown = q ? props.options.filter(o => `${o.label} ${o.hint ?? ''} ${o.group ?? ''}`.toLowerCase().includes(q)) : props.options
  const out: { title: string, options: HSelectOption[] }[] = []
  for (const o of shown) {
    const title = o.group ?? ''
    const g = out.find(x => x.title === title) ?? out[out.push({ title, options: [] }) - 1]!
    g.options.push(o)
  }
  return out
})

function pick(v: string) {
  model.value = v
  open.value = false
  query.value = ''
}
</script>

<template>
  <UPopover
    v-model:open="open"
    :content="{ side: 'bottom', align: 'start', sideOffset: 4 }"
    :disabled="disabled"
  >
    <button
      type="button"
      :aria-label="ariaLabel"
      :disabled="disabled"
      class="flex h-8 w-full min-w-0 items-center gap-2 border bg-default px-2.5 text-left text-[12.5px] outline-none disabled:opacity-60"
      :class="open ? 'border-accent' : 'border-accented hover:border-line focus-visible:border-accent'"
    >
      <span
        class="min-w-0 flex-1 truncate"
        :class="current ? 'text-white' : 'text-muted'"
      >{{ current?.label ?? placeholder }}</span>
      <span
        v-if="current?.hint"
        class="shrink-0 truncate text-[11.5px] text-muted"
      >{{ current.hint }}</span>
      <HIcon
        name="down"
        :size="12"
        class="shrink-0 text-muted"
      />
    </button>
    <template #content>
      <div
        role="listbox"
        :aria-label="ariaLabel"
        class="w-(--reka-popover-trigger-width) min-w-[220px] border border-accented bg-hover text-[12.5px]"
      >
        <label
          v-if="options.length >= FILTER_FROM"
          class="flex h-8 items-center gap-2 border-b border-default px-2.5 text-muted"
        >
          <HIcon
            name="search"
            :size="13"
          />
          <input
            v-model="query"
            placeholder="Filter"
            aria-label="Filter options"
            class="min-w-0 flex-1 bg-transparent text-white outline-none placeholder:text-muted"
          >
        </label>
        <div class="max-h-[280px] overflow-y-auto py-1">
          <template
            v-for="g in groups"
            :key="g.title"
          >
            <div
              v-if="g.title"
              class="px-2.5 pt-2 pb-1 text-[11px] text-muted"
            >
              {{ g.title }}
            </div>
            <div
              v-for="o in g.options"
              :key="o.value"
              role="option"
              :aria-selected="o.value === model"
              class="flex min-h-8 cursor-default items-center gap-2 border-l-2 pr-2.5 pl-[8px]"
              :class="o.value === model ? 'border-accent bg-raised text-white' : 'border-transparent text-toned hover:bg-raised hover:text-white'"
              @click="pick(o.value)"
            >
              <span class="min-w-0 flex-1 truncate">{{ o.label }}</span>
              <span
                v-if="o.hint"
                class="shrink-0 text-[11px] text-muted"
              >{{ o.hint }}</span>
            </div>
          </template>
          <p
            v-if="!groups.length"
            class="m-0 px-2.5 py-3 text-center text-muted"
          >
            Nothing matches.
          </p>
        </div>
      </div>
    </template>
  </UPopover>
</template>
