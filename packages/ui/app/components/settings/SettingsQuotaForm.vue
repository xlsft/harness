<script setup lang="ts">
// Two amounts and a Save: the daily and monthly spend limit of a user, a group or a role. Empty means no limit at this level.
import { limitText, parseLimit, type QuotaLimits } from '~/lib/enterprise/types'

const props = defineProps<{ limits: QuotaLimits, name: string, readonly?: boolean, hint?: string }>()
const emit = defineEmits<{ save: [QuotaLimits] }>()

const daily = ref('')
const monthly = ref('')
const error = ref('')
watch(() => props.limits, (l) => {
  daily.value = limitText(l.dailyUsd)
  monthly.value = limitText(l.monthlyUsd)
}, { immediate: true })

const dirty = computed(() => daily.value !== limitText(props.limits.dailyUsd) || monthly.value !== limitText(props.limits.monthlyUsd))

function save() {
  try {
    const next: QuotaLimits = {}
    const d = parseLimit(daily.value)
    const m = parseLimit(monthly.value)
    if (d !== undefined) next.dailyUsd = d
    if (m !== undefined) next.monthlyUsd = m
    error.value = ''
    emit('save', next)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

const input = 'h-7 w-[92px] border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent disabled:opacity-60'
</script>

<template>
  <form
    class="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[11.5px] text-muted"
    :aria-label="tt('Spend limits of {0}', [name])"
    @submit.prevent="save"
  >
    <label class="flex items-center gap-1.5">{{ tt('Per day $') }}
      <input
        v-model="daily"
        inputmode="decimal"
        placeholder="no limit"
        :disabled="readonly"
        :aria-label="tt('Daily limit of {0} in USD', [name])"
        :class="input"
      >
    </label>
    <label class="flex items-center gap-1.5">{{ tt('Per month $') }}
      <input
        v-model="monthly"
        inputmode="decimal"
        placeholder="no limit"
        :disabled="readonly"
        :aria-label="tt('Monthly limit of {0} in USD', [name])"
        :class="input"
      >
    </label>
    <HBtn
      v-if="!readonly"
      size="sm"
      type="submit"
      :disabled="!dirty"
    >
      {{ tt('Save') }}
    </HBtn>
    <span
      v-if="hint"
      class="text-dimmed"
    >{{ hint }}</span>
    <span
      v-if="error"
      role="alert"
      class="text-bad"
    >{{ error }}</span>
  </form>
</template>
