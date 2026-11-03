<script setup lang="ts">
// Model picker list (composer footer, new-session screen): search over every model of the connected
// providers (name, id, provider), starred favorites first, then models by provider. Favorites are kept in
// this browser. Enter picks the first match.
import type { ModelInfo } from '~/lib/transport'

defineProps<{ selected: string }>()
const emit = defineEmits<{ pick: [id: string] }>()

const store = useSessionStore()
const query = ref('')
const input = ref<HTMLInputElement | null>(null)
onMounted(() => input.value?.focus())

const FAVORITES = 'harness-favorite-models'
const favorites = ref<string[]>(read())
function read(): string[] {
  try {
    return JSON.parse(localStorage.getItem(FAVORITES) ?? '[]') as string[]
  } catch {
    return []
  }
}
function toggleFavorite(id: string) {
  favorites.value = favorites.value.includes(id) ? favorites.value.filter(x => x !== id) : [...favorites.value, id]
  try {
    localStorage.setItem(FAVORITES, JSON.stringify(favorites.value))
  } catch {
    // Not remembered.
  }
}

const nameOf = (m: ModelInfo) => m.displayName || m.id
const matches = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? store.models.filter(m => [m.id, m.displayName ?? '', m.provider].some(x => x.toLowerCase().includes(q))) : store.models
})
const sections = computed(() => {
  const out: { title: string, models: ModelInfo[] }[] = []
  const fav = matches.value.filter(m => favorites.value.includes(m.id))
  if (fav.length) out.push({ title: 'Favorites', models: fav })
  const byProvider = new Map<string, ModelInfo[]>()
  for (const m of matches.value) {
    if (fav.includes(m)) continue
    const p = m.provider || 'Models'
    if (!byProvider.has(p)) byProvider.set(p, [])
    byProvider.get(p)!.push(m)
  }
  for (const [title, models] of byProvider) out.push({ title, models })
  return out
})

function pickFirst() {
  const m = sections.value[0]?.models.find(x => !x.offline)
  if (m) emit('pick', m.id)
}

function ctx(n: number) {
  return n >= 1_000_000 ? `${n / 1_000_000}M` : `${Math.round(n / 1000)}k`
}
</script>

<template>
  <div>
    <label class="flex h-9 items-center gap-2.5 border-b border-default px-3 text-muted">
      <HIcon
        name="search"
        :size="13"
      />
      <input
        ref="input"
        v-model="query"
        placeholder="Search models"
        aria-label="Search models"
        class="min-w-0 flex-1 bg-transparent text-strong outline-none placeholder:text-muted"
        @keydown.enter.prevent="pickFirst"
      >
    </label>
    <div class="max-h-[340px] overflow-y-auto py-1">
      <template
        v-for="sec in sections"
        :key="sec.title"
      >
        <div class="px-3 pt-2.5 pb-1 text-[11px] text-muted first:pt-1.5">
          {{ sec.title }}
        </div>
        <div
          v-for="m in sec.models"
          :key="m.id"
          role="option"
          :aria-selected="m.id === selected"
          :aria-disabled="m.offline || undefined"
          class="group/model flex min-h-8 cursor-default items-center gap-3 border-l-2 pr-3 pl-[10px]"
          :class="m.id === selected ? 'border-accent bg-raised' : 'border-transparent hover:bg-raised'"
          @click="!m.offline && emit('pick', m.id)"
        >
          <span class="flex min-w-0 flex-1 items-baseline gap-2.5">
            <span
              class="shrink-0"
              :class="m.offline ? 'text-muted' : 'text-strong'"
            >{{ nameOf(m) }}</span>
            <span
              v-if="m.displayName && m.displayName !== m.id"
              class="min-w-0 truncate text-[11px] text-muted"
            >{{ m.id }}</span>
          </span>
          <span
            v-if="m.offline"
            class="text-[11px] text-orange"
          >offline</span>
          <span
            v-else-if="m.contextWindow"
            class="shrink-0 text-[11px] text-muted tabular-nums"
          >{{ ctx(m.contextWindow) }}{{ m.price ? ` · ${m.price}` : '' }}</span>
          <button
            type="button"
            :aria-label="favorites.includes(m.id) ? `Remove ${nameOf(m)} from favorites` : `Add ${nameOf(m)} to favorites`"
            :aria-pressed="favorites.includes(m.id)"
            class="flex h-6 shrink-0 items-center justify-center overflow-hidden transition-[width,margin] duration-150"
            :class="favorites.includes(m.id) ? 'w-6 text-warn' : '-ml-3 w-0 text-muted group-hover/model:ml-0 group-hover/model:w-6 hover:text-strong focus-visible:ml-0 focus-visible:w-6'"
            @click.stop="toggleFavorite(m.id)"
          >
            <HIcon
              :name="favorites.includes(m.id) ? 'star-filled' : 'star'"
              :size="14"
            />
          </button>
        </div>
      </template>
      <p
        v-if="!sections.length"
        class="m-0 px-3 py-5 text-center text-muted"
      >
        No models match.
      </p>
    </div>
  </div>
</template>
