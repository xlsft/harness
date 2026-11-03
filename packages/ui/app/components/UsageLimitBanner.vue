<script setup lang="ts">
// Above the composer when the session's provider is running out (SessionState.usageLimit): nearly used up, the
// agent wrapped up its turn because of it, or the provider refuses requests — with when it resets. Dismissed per
// state until the state changes.
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()
const limit = computed(() => props.session.usageLimit)
const dismissed = ref('')
const key = computed(() => limit.value ? `${props.session.meta.id}:${limit.value.state}:${limit.value.wrappedUp}:${limit.value.resetsAt}` : '')
const shown = computed(() => !!limit.value && limit.value.state !== 'cleared' && dismissed.value !== key.value)

const title = computed(() => {
  const l = limit.value
  if (!l) return ''
  if (l.state === 'reached') return `${l.providerName || 'The provider'} usage limit reached`
  if (l.wrappedUp) return 'Wrapped up near your usage limit'
  if (l.state === 'wrap_up') return 'Usage limit almost reached · the agent will wrap up'
  return 'Approaching your usage limit'
})
const detail = computed(() => {
  const l = limit.value
  if (!l) return ''
  const parts = [l.providerName && l.state !== 'reached' ? l.providerName : '', l.windowLabel && l.usedPercent ? `${l.windowLabel} ${Math.round(l.usedPercent)}% used` : '', l.balanceText]
  return parts.filter(Boolean).join(' · ')
})
const resets = computed(() => {
  const at = limit.value?.resetsAt
  if (!at) return ''
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return ''
  const sameDay = d.toDateString() === new Date().toDateString()
  return `Resets ${sameDay ? 'at' : ''} ${d.toLocaleString(undefined, sameDay ? { hour: 'numeric', minute: '2-digit' } : { weekday: 'short', hour: 'numeric', minute: '2-digit' })}`.replace('  ', ' ')
})
const bad = computed(() => limit.value?.state === 'reached')
</script>

<template>
  <div
    v-if="shown"
    role="status"
    class="flex items-start gap-2.5 border bg-surface px-3 py-2.5 text-[12.5px]"
    :class="bad ? 'border-bad' : 'border-default'"
  >
    <HIcon
      :name="bad ? 'alert' : limit!.wrappedUp ? 'check' : 'clock'"
      :size="15"
      class="mt-px shrink-0"
      :class="bad ? 'text-bad' : limit!.wrappedUp ? 'text-ok' : 'text-warn'"
    />
    <span class="flex min-w-0 flex-1 flex-col gap-0.5">
      <span class="flex flex-wrap items-baseline gap-x-2">
        <span class="text-strong">{{ title }}</span>
        <span
          v-if="resets"
          class="text-muted"
        >{{ resets }}</span>
      </span>
      <span
        v-if="detail"
        class="truncate text-[11.5px] text-muted"
      >{{ detail }}</span>
    </span>
    <HIconBtn
      icon="close"
      label="Dismiss"
      :size="26"
      :icon-size="12"
      @click="dismissed = key"
    />
  </div>
</template>
