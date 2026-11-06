<script setup lang="ts">
// Artifacts page (docs/design/ui/ArtifactsPage.dc.html), grid and list views, on demo fixtures.
// The view toggle emits `update:view`; the filter tabs are local.
import { ARTIFACT_FILTERS, ARTIFACT_KINDS, ARTIFACT_TYPES, ARTIFACTS, type ArtifactEntry } from '~/lib/fixtures/artifacts'

const props = defineProps<{ view: 'grid' | 'list' }>()
const emit = defineEmits<{ 'update:view': [view: 'grid' | 'list'] }>()

const filter = ref<typeof ARTIFACT_FILTERS[number]>('All')
const items = computed(() => ARTIFACTS.filter(a => filter.value === 'All' || (filter.value === 'Yours' ? !a.shared : a.shared)))

const open = { path: '/', query: { pane: 'artifact' } }

function type(a: ArtifactEntry) {
  return ARTIFACT_TYPES[a.type]
}
</script>

<template>
  <section
    :aria-label="tt('Artifacts')"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[1080px] flex-col gap-[22px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <h1 class="m-0 font-display text-[26px] font-bold text-strong">
        {{ tt('Artifacts') }}
      </h1>

      <div class="flex flex-wrap items-center gap-1">
        <div
          role="tablist"
          :aria-label="tt('Filter')"
          class="flex gap-[2px]"
        >
          <button
            v-for="f in ARTIFACT_FILTERS"
            :key="f"
            type="button"
            role="tab"
            :aria-selected="filter === f"
            class="border px-3 py-[5px] text-[12.5px]"
            :class="filter === f ? 'border-line bg-raised text-strong' : 'border-transparent text-toned hover:text-strong'"
            @click="filter = f"
          >
            {{ f }}
          </button>
        </div>
        <span class="flex-1" />
        <button
          type="button"
          :aria-label="tt('Search')"
          :title="tt('Search')"
          class="flex size-8 items-center justify-center text-toned hover:text-strong"
        >
          <HIcon
            name="search"
            :size="16"
          />
        </button>
        <button
          type="button"
          :aria-label="tt('Filter and sort')"
          :title="tt('Filter and sort')"
          class="flex size-8 items-center justify-center text-toned hover:text-strong"
        >
          <HIcon
            name="filter"
            :size="16"
          />
        </button>
        <button
          type="button"
          :aria-label="props.view === 'grid' ? tt('Show as list') : tt('Show as grid')"
          :title="props.view === 'grid' ? tt('Show as list') : tt('Show as grid')"
          class="flex size-8 items-center justify-center text-toned hover:text-strong"
          @click="emit('update:view', props.view === 'grid' ? 'list' : 'grid')"
        >
          <HIcon
            :name="props.view === 'grid' ? 'list' : 'grid'"
            :size="16"
          />
        </button>
      </div>

      <div class="flex flex-col gap-2.5">
        <span class="text-[12px] text-muted">{{ tt('Make something new') }}</span>
        <div class="flex flex-wrap gap-3.5">
          <a
            v-for="k in ARTIFACT_KINDS"
            :key="k.name"
            href="#"
            class="group flex w-[150px] flex-col gap-2 text-strong"
            @click.prevent
          >
            <span
              class="flex h-[92px] items-center justify-center border group-hover:brightness-125"
              :style="{ background: ARTIFACT_TYPES[k.type].tint, borderColor: ARTIFACT_TYPES[k.type].color, color: ARTIFACT_TYPES[k.type].color }"
            >
              <HIcon
                :name="ARTIFACT_TYPES[k.type].icon"
                :size="34"
              />
            </span>
            <span class="flex items-center gap-2 text-[12.5px]">{{ k.name }}<span
              class="border px-[5px] text-[10px]"
              :style="{ color: k.badgeColor, borderColor: k.badgeColor }"
            >{{ k.badge }}</span></span>
          </a>
        </div>
      </div>

      <div
        v-if="props.view === 'grid'"
        class="grid grid-cols-3 gap-3.5 pt-2 max-md:grid-cols-2 max-sm:grid-cols-1"
      >
        <NuxtLink
          v-for="a in items"
          :key="a.title"
          :to="open"
          class="flex flex-col border border-line bg-surface text-toned hover:border-line-strong"
        >
          <div
            class="flex h-[150px] items-end justify-center overflow-hidden border-b border-line px-7 pt-5"
            :style="{ background: type(a).tint }"
          >
            <div
              class="box-border flex h-[112px] w-full flex-col gap-[7px] border border-b-0 border-line p-3"
              :class="a.type === 'doc' ? 'bg-default' : 'bg-surface'"
            >
              <div
                v-if="a.type === 'design'"
                class="grid flex-1 grid-cols-3 gap-1.5"
              >
                <span class="bg-raised" /><span class="bg-raised" /><span class="bg-raised" /><span class="bg-hover" /><span class="bg-hover" /><span class="bg-hover" />
              </div>
              <template v-else-if="a.type === 'page'">
                <span
                  class="h-2 w-2/5"
                  :style="{ background: type(a).color }"
                /><span class="h-3.5 w-[70%] bg-line" /><span class="h-[5px] w-[90%] bg-raised" /><span class="h-[5px] w-4/5 bg-raised" /><span class="h-[5px] w-[85%] bg-raised" />
              </template>
              <template v-else-if="a.type === 'doc' || a.type === 'slides'">
                <span class="font-display text-[12px] leading-[1.3] text-strong">{{ a.title }}</span><span class="h-1 w-[92%] bg-raised" /><span class="h-1 w-[86%] bg-raised" /><span class="h-1 w-[90%] bg-raised" />
              </template>
              <div
                v-else-if="a.type === 'dash'"
                class="flex flex-1 items-end gap-1.5"
              >
                <span
                  v-for="h in [40, 70, 55, 90, 65]"
                  :key="h"
                  class="flex-1"
                  :style="{ height: `${h}%`, background: type(a).color }"
                />
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3 px-3.5 py-3">
            <span
              class="flex size-8 shrink-0 items-center justify-center"
              :style="{ background: type(a).tint, color: type(a).color }"
            >
              <HIcon :name="type(a).icon" />
            </span>
            <span class="flex min-w-0 flex-1 flex-col gap-[2px]"><span class="truncate text-[13px] text-strong">{{ a.title }}</span><span class="text-[11.5px] text-muted">{{ a.when }}</span></span>
            <HIcon
              name="globe-share"
              :class="a.shared ? 'text-toned' : 'text-transparent'"
              :aria-label="a.shared ? tt('Shared by link') : undefined"
              :aria-hidden="a.shared ? undefined : 'true'"
            />
          </div>
        </NuxtLink>
      </div>

      <div
        v-else
        role="list"
        class="flex flex-col pt-2"
      >
        <div
          v-for="a in items"
          :key="a.title"
          role="listitem"
          class="flex h-[46px] items-center gap-3 border-b border-muted"
        >
          <span
            class="flex size-7 shrink-0 items-center justify-center"
            :style="{ background: type(a).tint, color: type(a).color }"
          >
            <HIcon
              :name="type(a).icon"
              :size="14"
            />
          </span>
          <NuxtLink
            :to="open"
            class="min-w-0 flex-1 truncate text-[13px] text-strong hover:underline"
          >
            {{ a.title }}
          </NuxtLink>
          <span class="text-[12px] whitespace-nowrap text-muted">{{ a.when }}</span>
          <HIcon
            name="globe-share"
            :class="a.shared ? 'text-toned' : 'text-transparent'"
            :aria-label="a.shared ? tt('Shared by link') : undefined"
          />
          <button
            type="button"
            :aria-label="tt('More actions')"
            :title="tt('More actions')"
            class="ml-3 flex size-[30px] items-center justify-center text-muted hover:text-strong"
          >
            <HIcon name="more" />
          </button>
        </div>
      </div>
    </div>
  </section>
</template>
