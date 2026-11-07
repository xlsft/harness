<script setup lang="ts">
// Discover: a flat banner when there is something real to feature, sections of cards, and categories for skills and plugins. Everything
// comes from the client's answer; a section with nothing behind it isn't sent, so it isn't drawn.
import type { DiscoverExtensionsResponse, DiscoverItem, ExtensionType } from '~/lib/extensions/types'
import { shortDate } from '~/lib/extensions/view'

const props = defineProps<{
  data: DiscoverExtensionsResponse | null
  type: ExtensionType
  category: string
  section: string
  query: string
  loading: boolean
}>()
const emit = defineEmits<{ add: [DiscoverItem], open: [DiscoverItem], category: [string], section: [string], more: [cursor: string, key: string], refresh: [] }>()

const allCategories = ref(false)
const CATEGORY_ROWS = 9
const categories = computed(() => {
  const list = props.data?.categories ?? []
  return allCategories.value ? list : list.slice(0, CATEGORY_ROWS)
})
const showCategories = computed(() => !props.section && !props.category && !props.query && props.type !== 'connectors')
const nothing = computed(() => !props.loading && props.data && !props.data.sections.length && !props.data.featured)
const fetched = computed(() => shortDate(props.data?.registryFetchedAt ?? ''))
const more = ref('')

async function loadMore(cursor: string, key: string) {
  more.value = key
  emit('more', cursor, key)
  more.value = ''
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <p
      v-if="data?.registryProblem"
      class="m-0 flex items-center gap-2 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12.5px] text-toned"
    >
      <span class="flex-1">{{ tt('{0} What you see is what was saved.', [data.registryProblem]) }}</span>
      <HBtn
        size="xs"
        @click="emit('refresh')"
      >
        {{ tt('Try again') }}
      </HBtn>
    </p>
    <p
      v-if="data?.externalProblem"
      class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12.5px] text-toned"
    >
      {{ data.externalProblem }}
    </p>
    <p
      v-if="data?.externalBlockedByPolicy && type === 'skills'"
      class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12.5px] text-toned"
    >
      {{ tt('Your administrator turned external skill search off.') }}
    </p>

    <div
      v-if="category || section"
      class="flex items-center gap-2 text-[12.5px]"
    >
      <HBtn
        size="xs"
        @click="emit('category', ''); emit('section', '')"
      >
        <HIcon
          name="arrow-left"
          :size="11"
        />
        {{ tt('All') }}
      </HBtn>
      <span
        v-if="category"
        class="text-toned"
      >{{ category }}</span>
    </div>

    <!-- A flat band in the accent's colour: no picture, no gradient. -->
    <section
      v-if="data?.featured"
      :aria-label="tt('Featured')"
      class="flex flex-wrap items-center gap-x-6 gap-y-3 bg-accent/15 px-5 py-5"
      data-featured
    >
      <div class="flex min-w-0 flex-1 basis-[320px] flex-col gap-1">
        <span class="text-[11.5px] text-accent">{{ data.featured.sourceLabel === 'Default catalog' ? tt('From the default catalog') : tt('From the catalog') }}</span>
        <h2 class="m-0 text-[18px] font-semibold text-strong">
          {{ data.featured.name }}
        </h2>
        <p class="m-0 text-[12.5px] text-toned">
          {{ data.featured.description }}
        </p>
      </div>
      <HBtn
        variant="primary"
        size="md"
        @click="emit('add', data.featured)"
      >
        {{ tt('Add') }}
      </HBtn>
    </section>

    <section
      v-for="s in data?.sections ?? []"
      :key="s.key"
      class="flex flex-col gap-2.5"
      :data-section="s.key"
    >
      <div class="flex items-center gap-2">
        <h2 class="m-0 text-[13px] font-medium text-strong">
          {{ s.title }}
        </h2>
        <span
          v-if="s.total && !s.nextCursor"
          class="bg-raised px-1.5 text-[11px] text-muted"
        >{{ s.total }}</span>
        <span
          v-if="s.key === 'registry' && fetched"
          class="text-[11px] text-muted"
        >{{ tt('fetched {0}', [fetched]) }}</span>
        <span class="flex-1" />
        <button
          v-if="!section && !query && s.key !== 'results' && (s.total > s.items.length || s.nextCursor)"
          type="button"
          class="flex cursor-pointer items-center gap-1 text-[12px] text-muted hover:text-strong"
          @click="emit('section', s.key)"
        >
          {{ tt('Show all') }}
          <HIcon
            name="arrow-right"
            :size="12"
          />
        </button>
      </div>
      <div class="grid grid-cols-2 gap-2.5 max-md:grid-cols-1">
        <ExtensionsCard
          v-for="it in s.items"
          :key="it.id"
          :item="it"
          @open="emit('open', it)"
          @add="emit('add', it)"
        />
      </div>
      <div
        v-if="section && s.nextCursor"
        class="flex justify-center"
      >
        <HBtn
          size="sm"
          :disabled="more === s.key"
          @click="loadMore(s.nextCursor, s.key)"
        >
          {{ tt('Show more') }}
        </HBtn>
      </div>
    </section>

    <section
      v-if="showCategories && data?.categories.length"
      class="flex flex-col gap-2.5"
      data-section="categories"
    >
      <div class="flex items-center">
        <h2 class="m-0 flex-1 text-[13px] font-medium text-strong">
          {{ tt('Categories') }}
        </h2>
        <button
          v-if="data.categories.length > CATEGORY_ROWS"
          type="button"
          class="flex cursor-pointer items-center gap-1 text-[12px] text-muted hover:text-strong"
          :aria-expanded="allCategories"
          @click="allCategories = !allCategories"
        >
          {{ allCategories ? tt('Show fewer') : tt('Show all {0}', [data.categories.length]) }}
          <HIcon
            :name="allCategories ? 'up' : 'down'"
            :size="12"
          />
        </button>
      </div>
      <div class="grid grid-cols-3 gap-2.5 max-md:grid-cols-2 max-sm:grid-cols-1">
        <button
          v-for="c in categories"
          :key="c.name"
          type="button"
          class="flex cursor-pointer items-center gap-3 border border-default px-3 py-2.5 text-left hover:bg-hover"
          @click="emit('category', c.name)"
        >
          <span class="min-w-0 flex-1 truncate text-[12.5px] text-strong">{{ c.name }}</span>
          <span class="shrink-0 text-[11.5px] text-muted">{{ c.count }}</span>
        </button>
      </div>
    </section>

    <p
      v-if="nothing"
      class="m-0 text-[12.5px] text-muted"
    >
      {{ query || category ? tt('Nothing matches.') : tt('Nothing to show here yet. Add a marketplace to see what it offers.') }}
    </p>
    <HLoader
      v-if="loading"
      class="text-muted"
    />
  </div>
</template>
