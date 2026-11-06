<script setup lang="ts">
// Settings > Usage: your tokens, requests and spend over a range, as tiles, stacked bars by model, and tables by
// model and request kind. Admins can switch to everyone (with a per-user table) or one user. Money only counts
// for pay-as-you-go providers' priced models; the rest shows tokens only (lib/usage/types.ts).
import type { AdminUsageReport, UsageBucketPoint, UsageReport, UsageTotals } from '~/lib/usage/types'
import { downloadCsv } from '~/lib/enterprise/types'

const store = useSessionStore()
const access = useAccess()

const RANGES = [
  { value: '1', label: tt('Last 24 hours') },
  { value: '7', label: tt('Last 7 days') },
  { value: '30', label: tt('Last 30 days') },
  { value: '90', label: tt('Last 90 days') }
]
const range = ref('30')

// Admins: 'me', 'all' or a user id.
const isAdmin = ref(false)
const scope = ref('me')
const report = ref<UsageReport | null>(null)
const users = ref<AdminUsageReport['users']>([])
const knownUsers = ref<AdminUsageReport['users']>([])
const loading = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

const exporting = ref(false)

/** The records behind this view. Someone else's records on their personal providers come without model or cost. */
async function exportCsv() {
  if (!store.transport) return
  exporting.value = true
  try {
    const from = new Date(Date.now() - Number(range.value) * 86400_000).toISOString()
    const userId = scope.value === 'all' ? undefined : scope.value === 'me' ? await myId() : scope.value
    const r = await store.transport.enterprise().exportUsage({ from, userId })
    downloadCsv('harness-usage.csv', r.csv)
    error.value = r.truncated ? `Exported the newest ${r.rows} records: the export is limited. Pick a shorter range for the rest.` : ''
  } catch (e) {
    error.value = message(e)
  } finally {
    exporting.value = false
  }
}

async function myId() {
  try {
    return (await store.transport!.account().me()).id
  } catch {
    // The server narrows it to the caller for anyone who may not see more.
    return undefined
  }
}

async function load() {
  if (!store.transport) return
  loading.value = true
  const days = Number(range.value)
  const input = { from: new Date(Date.now() - days * 86400_000).toISOString() }
  try {
    if (scope.value === 'me') {
      report.value = await store.transport.usage().getMyUsage(input)
      users.value = []
    } else {
      const r = await store.transport.usage().getUsage(input, scope.value === 'all' ? undefined : scope.value)
      report.value = r.report
      users.value = r.users
      if (scope.value === 'all') knownUsers.value = r.users
    }
    error.value = ''
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  try {
    await access.load()
    isAdmin.value = access.can('usage.read')
  } catch {
    isAdmin.value = false
  }
  await load()
  // Users to pick from: everyone with usage in the last 90 days.
  if (isAdmin.value && store.transport) {
    try {
      knownUsers.value = (await store.transport.usage().getUsage({ from: new Date(Date.now() - 90 * 86400_000).toISOString() })).users
    } catch { /* the picker keeps Me / Everyone */ }
  }
})
watch([range, scope], load)

const scopes = computed(() => [
  { value: 'me', label: tt('Only me') },
  { value: 'all', label: tt('Everyone') },
  ...knownUsers.value.map(u => ({ value: u.userId, label: u.name || u.username || u.email, hint: u.username ? `@${u.username}` : undefined, group: 'Users' }))
])

// Formatting
const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 })
const tokens = (n: number) => n < 1000 ? String(n) : compact.format(n)
const money = (n: number) => n === 0 ? '$0' : n < 0.01 ? '<$0.01' : `$${n.toFixed(n < 10 ? 2 : n < 1000 ? 1 : 0)}`
// A cost is shown only where some tokens were priced; partially priced totals say so.
function cost(t: UsageTotals) {
  if (t.costUsd === 0 && t.unpricedTokens === t.totalTokens) return '—'
  return money(t.costUsd) + (t.unpricedTokens > 0 ? '+' : '')
}
const anyCost = computed(() => !!report.value && report.value.totals.unpricedTokens < report.value.totals.totalTokens)

// Series: the five largest models get the chart colours, the rest fold into "Other". A model keeps its colour
// while the range or scope changes (colour follows the model, not its rank).
const SLOTS = 5
const colourOf = new Map<string, number>()
const series = computed(() => {
  const models = report.value?.models ?? []
  const top = models.slice(0, SLOTS)
  const used = new Set<number>()
  for (const m of top) if (colourOf.has(m.key)) used.add(colourOf.get(m.key)!)
  for (const m of top) {
    if (colourOf.has(m.key) && !used.has(-1)) continue
    let slot = 0
    while (used.has(slot)) slot++
    if (slot < SLOTS) {
      colourOf.set(m.key, slot)
      used.add(slot)
    }
  }
  const out = top.map(m => ({ key: m.key, name: m.displayName, colour: `var(--color-chart-${(colourOf.get(m.key) ?? 0) + 1})` }))
  if (models.length > SLOTS) out.push({ key: '__other', name: 'Other', colour: 'var(--color-dimmed)' })
  return out
})
const seriesKey = (modelKey: string) => series.value.some(s => s.key === modelKey) ? modelKey : '__other'

// Bars: tokens (or cost) per bucket, stacked by series.
const metric = ref<'tokens' | 'cost'>('tokens')
watch(anyCost, (v) => {
  if (!v) metric.value = 'tokens'
})
const value = (t: UsageTotals) => metric.value === 'cost' ? t.costUsd : t.totalTokens
const bars = computed(() => (report.value?.buckets ?? []).map((b) => {
  const parts = new Map<string, number>()
  for (const m of b.models) {
    const k = seriesKey(m.modelKey)
    parts.set(k, (parts.get(k) ?? 0) + value(m.totals))
  }
  return { b, total: value(b.totals), parts: series.value.map(s => ({ ...s, v: parts.get(s.key) ?? 0 })).filter(p => p.v > 0) }
}))
// A rounded top for the axis: 1, 2 or 5 × 10^n.
const max = computed(() => {
  const m = Math.max(0, ...bars.value.map(b => b.total))
  if (m === 0) return 1
  const p = 10 ** Math.floor(Math.log10(m))
  return [1, 2, 5, 10].map(k => k * p).find(v => v >= m)!
})
const axisLabel = (v: number) => metric.value === 'cost' ? money(v) : tokens(v)

function bucketLabel(b: UsageBucketPoint, long = false) {
  const d = new Date(b.start)
  if (report.value?.bucket === 'hour') return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', ...(long ? { day: 'numeric', month: 'short' } : {}) })
  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', ...(long ? { weekday: 'short' } : {}) })
}
const ticks = computed(() => {
  const n = bars.value.length
  if (n < 2) return []
  return [0, Math.floor((n - 1) / 2), n - 1].map(i => ({ i, label: bucketLabel(bars.value[i]!.b) }))
})

const hover = ref<number | null>(null)
const hovered = computed(() => hover.value === null ? null : bars.value[hover.value] ?? null)

const CLASS_LABEL: Record<string, string> = { main: 'Conversation', subagent: 'Subagents', compaction: 'Compaction', auxiliary: 'Titles, classifier, summaries', other: 'Other' }
const classes = computed(() => {
  const all = report.value?.requestClasses ?? []
  const top = Math.max(1, ...all.map(c => c.totals.totalTokens))
  return all.map(c => ({ ...c, label: CLASS_LABEL[c.requestClass] ?? c.requestClass, share: c.totals.totalTokens / top }))
})

function ago(iso: string) {
  if (!iso) return '—'
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  return `${Math.round(s / 86400)} d ago`
}
</script>

<template>
  <h2 class="set-h">
    {{ tt('Usage') }}
  </h2>
  <p class="set-sub">
    {{ tt('Tokens and spend of {0} requests. Money counts only for pay-as-you-go models with a price; subscriptions and unpriced models show tokens.', [scope === 'me' ? 'your' : scope === 'all' ? 'everyone\'s' : 'this user\'s']) }}
  </p>

  <div class="mb-4 flex flex-wrap items-center gap-2">
    <div class="w-[170px]">
      <HSelect
        v-model="range"
        :options="RANGES"
        :aria-label="tt('Range')"
      />
    </div>
    <div
      v-if="isAdmin"
      class="w-[200px]"
    >
      <HSelect
        v-model="scope"
        :options="scopes"
        :aria-label="tt('Whose usage')"
      />
    </div>
    <HBtn
      size="sm"
      :disabled="exporting"
      @click="exportCsv"
    >
      <HIcon
        name="download"
        :size="12"
      />{{ tt('Export CSV') }}
    </HBtn>
    <div
      v-if="anyCost"
      role="radiogroup"
      :aria-label="tt('Chart shows')"
      class="ml-auto flex border border-line text-[12px]"
    >
      <button
        v-for="m in (['tokens', 'cost'] as const)"
        :key="m"
        type="button"
        role="radio"
        :aria-checked="metric === m"
        class="h-7 px-2.5"
        :class="metric === m ? 'bg-raised text-strong' : 'text-muted hover:text-strong'"
        @click="metric = m"
      >
        {{ m === 'tokens' ? tt('Tokens') : tt('Cost') }}
      </button>
    </div>
    <HIcon
      v-if="loading"
      name="loading"
      spin
      class="text-muted"
    />
  </div>

  <p
    v-if="error"
    class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
  >
    {{ error }}
  </p>

  <template v-if="report">
    <div class="grid grid-cols-4 border border-line max-md:grid-cols-2">
      <div class="flex flex-col gap-1 border-r border-line p-3 max-md:border-b">
        <span class="text-[11.5px] text-muted">{{ tt('Tokens') }}</span>
        <span class="text-[20px] font-semibold text-strong">{{ tokens(report.totals.totalTokens) }}</span>
        <span class="text-[11px] text-dimmed">{{ tt('{0} in · {1} out', [tokens(report.totals.inputTokens), tokens(report.totals.outputTokens)]) }}</span>
      </div>
      <div class="flex flex-col gap-1 border-r border-line p-3 max-md:border-r-0 max-md:border-b">
        <span class="text-[11.5px] text-muted">{{ tt('Cached') }}</span>
        <span class="text-[20px] font-semibold text-strong">{{ tokens(report.totals.cacheReadTokens) }}</span>
        <span class="text-[11px] text-dimmed">{{ tt('{0} written', [tokens(report.totals.cacheWriteTokens)]) }}</span>
      </div>
      <div class="flex flex-col gap-1 border-r border-line p-3">
        <span class="text-[11.5px] text-muted">{{ tt('Requests') }}</span>
        <span class="text-[20px] font-semibold text-strong">{{ report.totals.requests.toLocaleString() }}</span>
        <span class="text-[11px] text-dimmed">{{ report.models.length }} {{ report.models.length === 1 ? 'model' : 'models' }}</span>
      </div>
      <div class="flex flex-col gap-1 p-3">
        <span class="text-[11.5px] text-muted">{{ tt('Spend') }}</span>
        <span class="text-[20px] font-semibold text-strong">{{ anyCost ? money(report.totals.costUsd) : '—' }}</span>
        <span class="text-[11px] text-dimmed">{{ report.totals.unpricedTokens ? `${tokens(report.totals.unpricedTokens)} tokens not priced` : 'all priced' }}</span>
      </div>
    </div>

    <!-- Stacked bars, one per bucket; hover shows the bucket's split. -->
    <div class="relative mt-4 border border-line p-3 pb-2">
      <div class="mb-2 flex flex-wrap gap-x-3 gap-y-1 text-[11.5px] text-muted">
        <span
          v-for="s in series"
          :key="s.key"
          class="flex items-center gap-1.5"
        ><span
          class="size-2"
          :style="{ background: s.colour }"
        />{{ s.name }}</span>
      </div>
      <div class="relative flex h-[180px] pl-12">
        <div class="pointer-events-none absolute inset-0 left-12">
          <div
            v-for="f in [1, 0.5, 0]"
            :key="f"
            class="absolute inset-x-0 border-t border-line"
            :style="{ top: `${(1 - f) * 100}%` }"
          >
            <span class="absolute -top-2 -left-12 w-10 text-right text-[10.5px] text-dimmed">{{ axisLabel(max * f) }}</span>
          </div>
        </div>
        <div
          class="relative flex flex-1 items-end"
          @mouseleave="hover = null"
        >
          <div
            v-for="(bar, i) in bars"
            :key="bar.b.start"
            class="flex h-full min-w-0 flex-1 flex-col justify-end px-px"
            :class="hover === i ? 'bg-hover' : ''"
            @mouseenter="hover = i"
          >
            <div
              class="flex flex-col-reverse gap-[2px]"
              :style="{ height: `${(bar.total / max) * 100}%` }"
            >
              <div
                v-for="p in bar.parts"
                :key="p.key"
                class="min-h-px"
                :style="{ flexGrow: p.v, background: p.colour }"
              />
            </div>
          </div>
        </div>
      </div>
      <div class="relative mt-1 ml-12 h-4 text-[10.5px] text-dimmed">
        <span
          v-for="t in ticks"
          :key="t.i"
          class="absolute whitespace-nowrap"
          :style="t.i === 0 ? { left: 0 } : t.i === bars.length - 1 ? { right: 0 } : { left: `${((t.i + 0.5) / bars.length) * 100}%`, transform: 'translateX(-50%)' }"
        >{{ t.label }}</span>
      </div>
      <div
        v-if="hovered"
        class="pointer-events-none absolute top-3 right-3 z-10 min-w-[180px] border border-line-strong bg-hover px-2.5 py-2 text-[11.5px]"
      >
        <div class="mb-1 text-strong">
          {{ bucketLabel(hovered.b, true) }}
        </div>
        <div
          v-for="p in hovered.parts"
          :key="p.key"
          class="flex items-center gap-1.5 text-muted"
        >
          <span
            class="size-2"
            :style="{ background: p.colour }"
          /><span class="flex-1 truncate">{{ p.name }}</span><span class="text-strong">{{ axisLabel(p.v) }}</span>
        </div>
        <div class="mt-1 flex justify-between border-t border-line pt-1 text-muted">
          <span>{{ tt('{0} requests', [hovered.b.totals.requests]) }}</span><span class="text-strong">{{ metric === 'cost' ? cost(hovered.b.totals) : tokens(hovered.b.totals.totalTokens) }}</span>
        </div>
      </div>
      <p
        v-if="!report.totals.requests"
        class="absolute inset-0 m-0 flex items-center justify-center text-[12px] text-muted"
      >
        {{ tt('No requests in this range.') }}
      </p>
    </div>

    <template v-if="report.models.length">
      <h3 class="set-h mt-6 !text-[13px]">
        {{ tt('By model') }}
      </h3>
      <table class="mt-2 w-full border-collapse text-[12px]">
        <thead>
          <tr class="text-left text-[11.5px] text-muted">
            <th class="py-1.5 font-normal">
              {{ tt('Model') }}
            </th>
            <th class="py-1.5 text-right font-normal">
              {{ tt('Requests') }}
            </th>
            <th class="py-1.5 text-right font-normal">
              {{ tt('Tokens') }}
            </th>
            <th class="py-1.5 text-right font-normal">
              {{ tt('Cost') }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="m in report.models"
            :key="m.key"
            class="border-t border-line"
          >
            <td class="py-2">
              <span class="flex items-center gap-2">
                <span
                  class="size-2 shrink-0"
                  :style="{ background: series.find(s => s.key === seriesKey(m.key))?.colour }"
                />
                <span class="truncate text-strong">{{ m.displayName }}</span>
                <span class="truncate text-dimmed">{{ m.providerName }}</span>
              </span>
            </td>
            <td class="py-2 text-right">
              {{ m.totals.requests.toLocaleString() }}
            </td>
            <td class="py-2 text-right text-strong">
              {{ tokens(m.totals.totalTokens) }}
            </td>
            <td
              class="py-2 text-right"
              :title="m.priced ? (m.totals.unpricedTokens ? tt('Part of these tokens has no price') : '') : tt('No price, or a subscription: tokens only')"
            >
              {{ m.priced ? cost(m.totals) : 'tokens only' }}
            </td>
          </tr>
        </tbody>
      </table>

      <h3 class="set-h mt-6 !text-[13px]">
        {{ tt('By kind of request') }}
      </h3>
      <div class="mt-2 flex flex-col gap-2">
        <div
          v-for="c in classes"
          :key="c.requestClass"
          class="grid grid-cols-[200px_1fr_70px] items-center gap-3 text-[12px] max-sm:grid-cols-[1fr_70px]"
        >
          <span class="truncate text-muted">{{ c.label }}</span>
          <span class="h-2 bg-raised max-sm:hidden"><span
            class="block h-full bg-accent"
            :style="{ width: `${c.share * 100}%` }"
          /></span>
          <span class="text-right text-strong">{{ tokens(c.totals.totalTokens) }}</span>
        </div>
      </div>
    </template>

    <template v-if="users.length">
      <h3 class="set-h mt-6 !text-[13px]">
        {{ tt('By user') }}
      </h3>
      <table class="mt-2 w-full border-collapse text-[12px]">
        <thead>
          <tr class="text-left text-[11.5px] text-muted">
            <th class="py-1.5 font-normal">
              {{ tt('User') }}
            </th>
            <th class="py-1.5 text-right font-normal">
              {{ tt('Requests') }}
            </th>
            <th class="py-1.5 text-right font-normal">
              {{ tt('Tokens') }}
            </th>
            <th class="py-1.5 text-right font-normal">
              {{ tt('Cost') }}
            </th>
            <th class="py-1.5 text-right font-normal">
              {{ tt('Last active') }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="u in users"
            :key="u.userId"
            class="cursor-pointer border-t border-line hover:bg-hover"
            @click="scope = u.userId"
          >
            <td class="py-2">
              <span class="text-strong">{{ u.name || u.username || u.email }}</span>
              <span
                v-if="u.username"
                class="ml-2 text-dimmed"
              >@{{ u.username }}</span>
            </td>
            <td class="py-2 text-right">
              {{ u.totals.requests.toLocaleString() }}
            </td>
            <td class="py-2 text-right text-strong">
              {{ tokens(u.totals.totalTokens) }}
            </td>
            <td class="py-2 text-right">
              {{ cost(u.totals) }}
            </td>
            <td class="py-2 text-right text-muted">
              {{ ago(u.lastActiveAt) }}
            </td>
          </tr>
        </tbody>
      </table>
    </template>
  </template>
</template>
