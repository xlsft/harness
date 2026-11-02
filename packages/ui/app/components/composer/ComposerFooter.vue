<script setup lang="ts">
// Composer footer (Composer.dc.html): hint · model · effort · mode · usage ring, with the model and mode pickers.
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState, hint?: string, initialMenu?: 'mode' | 'model' | '' }>()
const store = useSessionStore()

const modeOpen = ref(props.initialMenu === 'mode')
const modelOpen = ref(props.initialMenu === 'model')
const query = ref('')

const id = computed(() => props.session.meta.id)
const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
const effort = computed(() => store.effort[id.value] ?? 'medium')
const contextWindow = computed(() => model.value?.contextWindow || 200_000)
const pct = computed(() => Math.min(100, Math.round((props.session.contextTokens / contextWindow.value) * 100)))
const groups = computed(() => {
  const map = new Map<string, typeof store.models>()
  for (const m of store.models) {
    if (query.value && !m.id.includes(query.value.toLowerCase())) continue
    const p = m.provider || 'Models'
    if (!map.has(p)) map.set(p, [])
    map.get(p)!.push(m)
  }
  return [...map.entries()]
})

function ctx(n: number) {
  return n >= 1_000_000 ? `${n / 1_000_000}M` : `${Math.round(n / 1000)}k`
}

const BTN = 'flex shrink-0 items-center gap-[5px] border px-1.5 py-0.5 text-[11.5px] whitespace-nowrap text-muted hover:text-toned'
</script>

<template>
  <div class="flex min-w-0 flex-nowrap items-center gap-0.5 text-[11.5px] whitespace-nowrap text-muted">
    <span class="min-w-0 flex-1 truncate">{{ hint }}</span>

    <UPopover
      v-model:open="modelOpen"
      :content="{ side: 'top', align: 'end', sideOffset: 6 }"
    >
      <button
        type="button"
        aria-label="Model"
        :class="[BTN, modelOpen ? 'border-accent' : 'border-transparent']"
      >
        <span class="text-white">{{ session.meta.model }}</span><span class="max-sm:hidden">· {{ (model?.provider || 'openrouter').toLowerCase().replace(' · local', '') }}</span>
        <HIcon
          name="down"
          :size="11"
        />
      </button>
      <template #content>
        <div
          role="listbox"
          aria-label="Model"
          class="w-[380px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
        >
          <label class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-muted">
            <HIcon
              name="search"
              :size="13"
            />
            <input
              v-model="query"
              placeholder="Search models"
              class="min-w-0 flex-1 bg-transparent text-white outline-none placeholder:text-muted"
            >
          </label>
          <template
            v-for="[provider, models] in groups"
            :key="provider"
          >
            <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
              {{ provider }}
            </div>
            <div
              v-for="m in models"
              :key="m.id"
              role="option"
              :aria-selected="m.id === session.meta.model"
              :aria-disabled="m.offline || undefined"
              class="flex cursor-default gap-2.5 border-l-2 px-2.5 py-[5px]"
              :class="m.id === session.meta.model ? 'border-accent bg-raised' : 'border-transparent hover:bg-raised'"
              @click="!m.offline && (store.setModel(id, m.id), modelOpen = false)"
            >
              <span
                class="flex-1"
                :class="m.offline ? 'text-muted' : 'text-white'"
              >{{ m.id }}</span>
              <span
                v-if="m.offline"
                class="text-[11px] text-orange"
              >offline</span>
              <span
                v-else
                class="text-[11px] text-muted"
              >{{ ctx(m.contextWindow) }}{{ m.price ? ` · ${m.price}` : '' }}</span>
            </div>
          </template>
          <div class="flex gap-2.5 border-t border-default px-2.5 py-1.5 text-[11.5px] text-muted">
            <span>effort</span>
            <button
              v-for="e in EFFORTS"
              :key="e"
              type="button"
              class="border px-1.5"
              :class="e === effort ? 'border-accent text-white' : 'border-default'"
              @click="store.setEffort(id, e)"
            >
              {{ e }}
            </button>
          </div>
        </div>
      </template>
    </UPopover>

    <button
      type="button"
      aria-label="Effort"
      :class="[BTN, 'border-transparent max-sm:hidden']"
      @click="modelOpen = true"
    >
      effort <span class="text-white">{{ effort }}</span><HIcon
        name="down"
        :size="11"
      />
    </button>

    <UPopover
      v-model:open="modeOpen"
      :content="{ side: 'top', align: 'end', sideOffset: 6 }"
    >
      <button
        type="button"
        aria-label="Permission mode"
        :class="[BTN, modeOpen ? 'border-accent' : 'border-transparent']"
      >
        mode <span class="text-white">{{ MODE_LABEL[session.meta.permissionMode] }}</span><HIcon
          name="down"
          :size="11"
        />
      </button>
      <template #content>
        <div
          role="listbox"
          aria-label="Permission mode"
          class="w-[340px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
        >
          <div
            v-for="m in MODES"
            :key="m.key"
            role="option"
            :aria-selected="m.key === session.meta.permissionMode"
            class="flex cursor-default flex-col border-l-2 px-2.5 py-1.5"
            :class="[m.key === session.meta.permissionMode ? 'border-l-accent bg-raised' : 'border-l-transparent hover:bg-raised', m.sep ? 'border-t border-t-default' : '']"
            @click="store.setPermissionMode(id, m.key); modeOpen = false"
          >
            <span :class="m.cls">{{ m.label }}</span><span class="text-[11px] text-muted">{{ m.detail }}</span>
          </div>
        </div>
      </template>
    </UPopover>

    <button
      type="button"
      :aria-label="`Usage limits: ${pct}% used`"
      :class="[BTN, 'border-transparent']"
    >
      <svg
        viewBox="0 0 20 20"
        class="size-[15px]"
        aria-hidden="true"
      >
        <circle
          cx="10"
          cy="10"
          r="8"
          fill="none"
          stroke="#2b2b2b"
          stroke-width="3"
        />
        <circle
          cx="10"
          cy="10"
          r="8"
          fill="none"
          stroke="#2b7fff"
          stroke-width="3"
          :stroke-dasharray="`${(pct / 100) * 50.27} 50.27`"
          transform="rotate(-90 10 10)"
        />
      </svg>
      <span class="text-white">{{ pct }}%</span>
    </button>
  </div>
</template>
