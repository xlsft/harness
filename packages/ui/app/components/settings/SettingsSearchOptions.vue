<script setup lang="ts">
// Options of a connected search provider (result count, safe search, country, language): only those its
// service supports. Each change saves at once; an empty field follows the service default.
import type { PresetOptionView } from '~/lib/providers/types'

const props = defineProps<{
  specs: PresetOptionView[]
  values: Record<string, string>
  name: string
  disabled?: boolean
}>()
const emit = defineEmits<{ save: [options: Record<string, string>] }>()

// Text and number fields save on blur or Enter, when they changed.
const drafts = reactive<Record<string, string | number>>({})
watch(() => props.values, (v) => {
  for (const o of props.specs) drafts[o.name] = v[o.name] ?? ''
}, { immediate: true, deep: true })

// A number field's v-model yields a number.
function commit(name: string, raw: string | number | undefined) {
  const value = String(raw ?? '').trim()
  if ((props.values[name] ?? '') === value) return
  emit('save', { ...props.values, [name]: value })
}

const DEFAULT = '__default__'
function selectOptions(o: PresetOptionView) {
  const def = o.choices.find(c => c.value === o.defaultValue)?.label
  return [{ value: DEFAULT, label: def ? `Service default (${def})` : 'Service default' }, ...o.choices]
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-white outline-none focus:border-accent disabled:opacity-60'
</script>

<template>
  <div
    class="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-3 pr-3 pb-3 pl-[52px] max-sm:pl-3"
    role="group"
    :aria-label="`${name} options`"
  >
    <label
      v-for="o in specs"
      :key="o.name"
      class="flex flex-col gap-1 text-[11.5px] text-muted"
    >
      {{ o.label }}
      <HSelect
        v-if="o.kind === 'select'"
        :model-value="values[o.name] || DEFAULT"
        :aria-label="`${name}: ${o.label}`"
        :options="selectOptions(o)"
        :disabled="disabled"
        @update:model-value="v => commit(o.name, v === DEFAULT ? '' : v)"
      />
      <input
        v-else
        v-model="drafts[o.name]"
        :type="o.kind === 'number' ? 'number' : 'text'"
        :min="o.kind === 'number' ? o.min : undefined"
        :max="o.kind === 'number' ? o.max : undefined"
        :placeholder="o.placeholder || (o.defaultValue ? `Default ${o.defaultValue}` : 'Service default')"
        :aria-label="`${name}: ${o.label}`"
        :disabled="disabled"
        :class="input"
        @blur="commit(o.name, drafts[o.name])"
        @keydown.enter.prevent="commit(o.name, drafts[o.name])"
      >
    </label>
  </div>
</template>
