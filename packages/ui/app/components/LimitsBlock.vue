<script setup lang="ts">
// What the session's provider has left: its balance (credit read from the provider, or an estimate), this month's spend
// against a budget, and its plan's usage windows (5h, weekly) — or, for a session an ACP agent runs, the
// subscription limits the agent reports. The server leaves out what the user may not see; an ACP agent's limits
// never leave this computer. Renders nothing when there is nothing to show.
import type { LimitWindowView, ProviderLimitsView } from '~/lib/limits/types'
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()

const view = ref<ProviderLimitsView | null>(null)
const acp = computed(() => props.session.meta.acp?.limits)

let request = 0
async function load() {
  view.value = null
  if (acp.value || props.session.meta.remote || !store.transport || !props.session.meta.model) return
  const mine = ++request
  try {
    const res = await store.transport.limits().getProviderLimits({ modelId: props.session.meta.model })
    if (mine === request) view.value = res.model ?? null
  } catch { /* no server, or nothing visible: no block */ }
}
onMounted(load)
watch(() => [props.session.meta.id, props.session.meta.model], load)

const windows = computed<LimitWindowView[]>(() => acp.value
  ? acp.value.windows.map(w => ({ label: w.label, usedPercent: w.usedPercent, resetsAt: w.resetsAt }))
  : view.value?.windows ?? [])
const title = computed(() => acp.value ? `${props.session.meta.acp?.agentName || 'Agent'}${acp.value.planName ? ` · ${acp.value.planName}` : ''}` : view.value?.providerName ?? '')

function money(n: number, currency = 'USD') {
  return new Intl.NumberFormat(undefined, { style: 'currency', currency, maximumFractionDigits: n < 10 ? 2 : 0 }).format(n)
}
function resets(at: string) {
  const ms = Date.parse(at) - Date.now()
  if (!at || Number.isNaN(ms) || ms <= 0) return ''
  const h = Math.floor(ms / 3_600_000)
  return h >= 24 ? `resets in ${Math.round(h / 24)}d` : h ? `resets in ${h}h` : `resets in ${Math.max(1, Math.round(ms / 60_000))}m`
}
// Windows read as words, whatever short name the provider or the agent gives them.
const WINDOW_NAMES: Record<string, string> = {
  '1m': 'Per minute', '1h': 'Hourly', '5h': '5-hour limit', '1d': 'Daily', '24h': 'Daily', '7d': 'Weekly', '30d': 'Monthly',
  '7d Opus': 'Weekly · Opus', '7d Sonnet': 'Weekly · Sonnet'
}
const windowName = (label: string) => WINDOW_NAMES[label] ?? label
const TRACK = 'block h-1 overflow-hidden rounded-chip bg-raised'
const BAR = 'block h-full rounded-chip'
const tone = (pct: number) => pct >= 90 ? 'bg-bad' : pct >= 70 ? 'bg-warn' : 'bg-accent'
const balance = computed(() => view.value?.balance)
const spend = computed(() => view.value?.spend)
const hasBalance = computed(() => !!balance.value && !balance.value.error && balance.value.balance !== undefined)
// An estimate shows its cents: it moves by small amounts, and they are how you see it is alive.
const balanceText = computed(() => {
  const b = balance.value!
  if (!b.estimated) return money(b.balance!, b.currency)
  return `~${new Intl.NumberFormat(undefined, { style: 'currency', currency: b.currency, minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(b.balance!)}`
})
const balanceHint = computed(() => {
  const b = balance.value!
  if (!b.estimated) return undefined
  const since = b.estimatedSince ? new Date(b.estimatedSince).toLocaleDateString() : ''
  return `An estimate: ${money(b.total ?? 0, b.currency)} entered${since ? ` on ${since}` : ''}, minus what requests through harness cost since. Spending elsewhere isn't seen.`
})
const balanceLeft = computed(() => Math.max(0, Math.min(100, (balance.value!.balance! / balance.value!.total!) * 100)))
const shown = computed(() => !!windows.value.length || (balance.value && !balance.value.error && balance.value.balance !== undefined) || !!spend.value)
</script>

<template>
  <div
    v-if="shown"
    class="-mx-[13px] mt-3 flex flex-col gap-3 border-t border-default px-[13px] pt-3 text-[12px]"
  >
    <span class="truncate text-muted">{{ title || tt('Limits') }}</span>
    <!-- Money on one line: what this month cost, and what is left. -->
    <div
      v-if="spend || hasBalance"
      class="flex flex-col gap-1.5"
    >
      <div class="flex items-baseline justify-between gap-4">
        <span
          v-if="spend"
          class="flex min-w-0 items-baseline gap-1.5"
        >
          <span class="truncate text-strong">{{ tt('This month') }}</span>
          <span
            class="shrink-0 text-muted tabular-nums"
            :title="spend.partial ? tt('Some requests had no price; this is a lower bound') : undefined"
          >{{ spend.partial ? '≥' : '' }}{{ money(spend.spent, spend.currency) }}<template v-if="spend.budget"> {{ tt('of {0}', [money(spend.budget, spend.currency)]) }}</template></span>
        </span>
        <span
          v-if="hasBalance"
          class="flex min-w-0 items-baseline gap-1.5"
        >
          <span class="truncate text-strong">{{ tt('Balance') }}</span>
          <span
            class="shrink-0 text-muted tabular-nums"
            :title="balanceHint"
          >{{ balanceText }}<template v-if="balance!.total && !balance!.estimated"> {{ tt('of {0}', [money(balance!.total, balance!.currency)]) }}</template></span>
        </span>
      </div>
      <span
        v-if="spend?.budget"
        :class="TRACK"
      ><span
        :class="[BAR, tone((spend.spent / spend.budget) * 100)]"
        :style="{ width: `${Math.min(100, (spend.spent / spend.budget) * 100)}%` }"
      /></span>
      <span
        v-if="hasBalance && balance!.total"
        :class="TRACK"
      ><span
        :class="[BAR, tone(100 - balanceLeft)]"
        :style="{ width: `${balanceLeft}%` }"
      /></span>
    </div>
    <div
      v-for="w in windows"
      :key="w.label"
      class="flex flex-col gap-1.5"
    >
      <div class="flex items-baseline gap-2">
        <span class="min-w-0 flex-1 truncate text-strong">{{ windowName(w.label) }}</span>
        <span class="shrink-0 text-[11.5px] text-muted">{{ resets(w.resetsAt) }}</span>
        <span class="shrink-0 text-muted tabular-nums">{{ Math.round(w.usedPercent) }}%</span>
      </div>
      <span :class="TRACK"><span
        :class="[BAR, tone(w.usedPercent)]"
        :style="{ width: `${Math.min(100, w.usedPercent)}%` }"
      /></span>
    </div>
  </div>
</template>
