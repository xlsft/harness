<script setup lang="ts">
// The Context section of the session details: one row (percentage and a bar) that expands into the breakdown
// the client computes (AgentService.GetContextUsage): categories with a segmented bar, deferred tool definitions,
// the MCP tools and what is left until auto-compaction, with a Compact session button. The breakdown is fetched
// when the row opens and again after each turn or compaction while it stays open.
import type { SessionState } from '~/lib/session/types'
import { categorySwatches, mcpToolLabel, percentOf, type ContextUsageVM } from '~/lib/session/context'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()

const open = ref(false)
const toolsOpen = ref(false)
const usage = ref<ContextUsageVM | null>(null)
const loading = ref(false)
const error = ref('')

const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
// Collapsed, the row uses the last reported usage; the breakdown's own count once it is loaded.
const ctxWindow = computed(() => usage.value?.window || model.value?.contextWindow || 200_000)
const used = computed(() => usage.value?.used ?? props.session.contextTokens)
const pct = computed(() => Math.min(100, percentOf(used.value, ctxWindow.value)))
const swatches = computed(() => usage.value ? categorySwatches(usage.value.categories) : [])
const compacting = computed(() => !!store.compacting[props.session.meta.id] || !!usage.value?.compacting)
const canCompact = computed(() => !props.session.meta.remote && props.session.meta.status === 'idle' && !compacting.value)

let request = 0
async function load() {
  const id = props.session.meta.id
  const mine = ++request
  loading.value = true
  try {
    const u = await store.contextUsage(id)
    if (mine === request) {
      usage.value = u
      error.value = ''
    }
  } catch (e) {
    if (mine === request) error.value = props.session.meta.remote ? 'The breakdown is available on the session\'s machine.' : message(e)
  } finally {
    if (mine === request) loading.value = false
  }
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function toggle() {
  open.value = !open.value
  if (open.value) void load()
}

async function compact() {
  error.value = ''
  try {
    await store.compact(props.session.meta.id)
  } catch (e) {
    error.value = message(e)
  }
  if (open.value) void load()
}

// Refresh while open: after a turn (the session is idle again) and after a compaction.
const compactions = computed(() => props.session.items.filter(i => i.kind === 'compact').length)
watch([() => props.session.meta.status === 'idle', compactions], ([idle], [wasIdle, before]) => {
  if (open.value && ((idle && !wasIdle) || compactions.value !== before)) void load()
})
// Another session: drop the old breakdown.
watch(() => props.session.meta.id, () => {
  usage.value = null
  error.value = ''
  toolsOpen.value = false
  if (open.value) void load()
})

// 698.2k, 1M: one decimal, so the numbers move as the context grows.
function tok(n: number) {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, '')}k`
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`
}

function share(n: number) {
  const w = ctxWindow.value
  if (w <= 0) return '0%'
  const p = (n / w) * 100
  return `${p > 0 && p < 0.1 ? '<0.1' : p.toFixed(1).replace(/\.0$/, '')}%`
}
</script>

<template>
  <section class="flex flex-col border-b border-default px-[13px] py-3">
    <button
      type="button"
      class="-mx-[9px] -my-1 flex items-center gap-2 rounded-none px-[9px] py-1 text-left hover:bg-hover"
      :aria-expanded="open"
      @click="toggle"
    >
      <span class="text-[12px] text-muted">Context</span>
      <HIcon
        name="right"
        :size="12"
        class="shrink-0 text-muted transition-transform"
        :class="open && 'rotate-90'"
      />
      <span class="flex-1" />
      <span class="shrink-0 text-[12px] text-strong tabular-nums">{{ pct }}%</span>
      <span class="flex h-1 w-12 shrink-0 bg-raised">
        <span
          class="bg-accent"
          :style="{ width: `${pct}%` }"
        />
      </span>
    </button>

    <div
      v-if="open"
      class="flex flex-col pt-2.5"
    >
      <p
        v-if="error"
        role="alert"
        class="m-0 truncate text-[11.5px] text-muted"
        :title="error"
      >
        {{ error }}
      </p>
      <p
        v-else-if="!usage"
        class="m-0 text-[11.5px] text-muted"
      >
        {{ loading ? 'Counting…' : '' }}
      </p>

      <template v-if="usage">
        <div class="flex items-baseline gap-2 text-[12px]">
          <span class="shrink-0 text-strong tabular-nums">{{ percentOf(usage.used, usage.window) }}% used</span>
          <span class="flex-1" />
          <span
            class="min-w-0 truncate text-muted tabular-nums"
            :title="usage.estimated ? 'Estimated: no provider count yet' : undefined"
          >{{ usage.estimated ? '~' : '' }}{{ tok(usage.used) }} / {{ tok(usage.window) }}</span>
        </div>

        <div
          class="mt-2 flex h-1.5 overflow-hidden bg-raised"
          role="img"
          :aria-label="`${percentOf(usage.used, usage.window)}% of the context window used`"
        >
          <span
            v-for="(c, i) in usage.categories"
            :key="c.key"
            :class="swatches[i]"
            :style="{ width: `${usage.window ? (c.tokens / usage.window) * 100 : 0}%` }"
          />
        </div>

        <ul class="m-0 mt-2 flex list-none flex-col p-0">
          <li
            v-for="(c, i) in usage.categories"
            :key="c.key"
            class="flex min-h-6 items-center gap-2 text-[12px]"
          >
            <span
              class="size-2 shrink-0"
              :class="swatches[i]"
            />
            <span class="min-w-0 flex-1 truncate">{{ c.label }}</span>
            <span class="shrink-0 text-strong tabular-nums">{{ tok(c.tokens) }}</span>
            <span class="w-11 shrink-0 text-right text-muted tabular-nums">{{ share(c.tokens) }}</span>
          </li>
          <li
            v-for="d in [{ label: 'MCP tools (deferred)', tokens: usage.deferredMcpTools }, { label: 'System tools (deferred)', tokens: usage.deferredSystemTools }]"
            :key="d.label"
            class="flex min-h-6 items-center gap-2 text-[12px]"
          >
            <span class="size-2 shrink-0 border border-dimmed" />
            <span class="min-w-0 flex-1 truncate text-muted">{{ d.label }}</span>
            <span class="shrink-0 text-strong tabular-nums">{{ tok(d.tokens) }}</span>
            <span class="w-11 shrink-0" />
          </li>
        </ul>

        <template v-if="usage.mcpTools.length">
          <button
            type="button"
            class="-mx-[9px] mt-1 flex items-center gap-2 rounded-none px-[9px] py-1 text-left text-[12px] hover:bg-hover"
            :aria-expanded="toolsOpen"
            @click="toolsOpen = !toolsOpen"
          >
            <span class="text-muted">MCP tools</span>
            <HIcon
              name="right"
              :size="12"
              class="shrink-0 text-muted transition-transform"
              :class="toolsOpen && 'rotate-90'"
            />
            <span class="flex-1" />
            <span class="shrink-0 text-muted tabular-nums">{{ usage.mcpTools.length }}</span>
          </button>
          <ul
            v-if="toolsOpen"
            class="m-0 flex list-none flex-col p-0"
          >
            <li
              v-for="t in usage.mcpTools"
              :key="t.name"
              class="flex min-h-6 items-center gap-2 text-[12px]"
              :title="t.deferred ? `${t.name} (deferred: loaded on demand)` : t.name"
            >
              <span
                class="min-w-0 flex-1 truncate"
                :class="t.deferred && 'text-muted'"
              >{{ mcpToolLabel(t) }}</span>
              <span class="shrink-0 text-strong tabular-nums">{{ tok(t.tokens) }}</span>
            </li>
          </ul>
        </template>

        <div class="mt-2 flex items-center gap-2">
          <span class="min-w-0 flex-1 truncate text-[11.5px] text-muted tabular-nums">{{ usage.autocompactThreshold ? `${tok(usage.untilAutocompact)} until auto-compact` : 'Auto-compact off' }}</span>
          <HBtn
            :disabled="!canCompact"
            :aria-busy="compacting"
            @click="compact"
          >
            {{ compacting ? 'Compacting…' : 'Compact session' }}
          </HBtn>
        </div>
      </template>
    </div>
  </section>
</template>
