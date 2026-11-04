<script setup lang="ts">
// Model picker list (composer footer, new-session screen): search over every model of the connected
// providers (name, id, provider), starred favorites first, then models by provider. Favorites are kept in
// this browser. Enter picks the first match. Below the server's providers comes a group per connected ACP agent,
// with what the scope allows (~/lib/acp/modelChoices): a harness session shows them disabled, a session on an agent
// only its own. Agents' model ids are their own, so everything here goes by a row's key, and a pick says the engine.
import { firstPickable, modelKey, modelSections, type ModelScope } from '~/lib/acp/modelChoices'

const props = withDefaults(defineProps<{ selected: string, selectedEngine?: string, scope?: ModelScope }>(), { selectedEngine: '', scope: () => ({ kind: 'new' }) })
const emit = defineEmits<{ pick: [id: string, engine: string] }>()

const store = useSessionStore()
const query = ref('')
const input = ref<HTMLInputElement | null>(null)
onMounted(() => {
  input.value?.focus()
  // What the agents offer now; a probe it starts shows when it finishes.
  if (props.scope.kind !== 'server') store.quietly(store.refreshAcpAgents({ cachedOnly: store.acpLoaded }))
})

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

const sections = computed(() => modelSections({
  models: store.models,
  agents: store.acpAgents,
  refusal: store.acpSwitchRefusal,
  scope: props.scope,
  query: query.value,
  favorites: favorites.value
}))
const selectedKey = computed(() => modelKey(props.selected, props.selectedEngine))
const pickable = (m: { offline: boolean, disabledReason: string }) => !m.offline && !m.disabledReason

function pickFirst() {
  const m = firstPickable(sections.value)
  if (m) emit('pick', m.id, m.engine)
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
        :key="sec.key"
      >
        <div class="px-3 pt-2.5 pb-1 text-[11px] text-muted first:pt-1.5">
          {{ sec.title }}
        </div>
        <div
          v-if="sec.note"
          class="flex items-baseline gap-2 px-3 pb-1 text-[11px]"
          :class="sec.failed ? 'text-warn' : 'text-muted'"
        >
          <span class="min-w-0 flex-1">{{ sec.note }}</span>
          <button
            v-if="sec.failed"
            type="button"
            class="shrink-0 text-toned hover:text-strong"
            @click="store.quietly(store.refreshAcpAgents({ refresh: true }))"
          >
            Try again
          </button>
        </div>
        <div
          v-for="m in sec.rows"
          :key="m.key"
          role="option"
          :aria-selected="m.key === selectedKey"
          :aria-disabled="!pickable(m) || undefined"
          :title="m.disabledReason || undefined"
          class="group/model flex min-h-8 items-center gap-3 rounded-none border-l-2 pr-3 pl-[10px]"
          :class="[m.key === selectedKey ? 'border-accent bg-raised' : 'border-transparent', pickable(m) && 'cursor-pointer', pickable(m) && m.key !== selectedKey && 'hover:bg-raised']"
          @click="pickable(m) && emit('pick', m.id, m.engine)"
        >
          <span class="flex min-w-0 flex-1 items-baseline gap-2.5">
            <span
              class="shrink-0"
              :class="pickable(m) ? 'text-strong' : 'text-muted'"
            >{{ m.name }}</span>
            <span
              v-if="m.detail"
              class="min-w-0 truncate text-[11px] text-muted"
            >{{ m.detail }}</span>
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
            :aria-label="favorites.includes(m.key) ? `Remove ${m.name} from favorites` : `Add ${m.name} to favorites`"
            :aria-pressed="favorites.includes(m.key)"
            class="flex h-6 shrink-0 items-center justify-center overflow-hidden transition-[width,margin] duration-150"
            :class="favorites.includes(m.key) ? 'w-6 text-warn' : '-ml-3 w-0 text-muted group-hover/model:ml-0 group-hover/model:w-6 hover:text-strong focus-visible:ml-0 focus-visible:w-6'"
            @click.stop="toggleFavorite(m.key)"
          >
            <HIcon
              :name="favorites.includes(m.key) ? 'star-filled' : 'star'"
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
