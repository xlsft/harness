<script setup lang="ts">
// The pinned row: type tabs, Yours or Discover, search, filter, sort and Add.
import type { RowMenuItem } from '~/components/RowMenu.vue'
import type { ExtensionType, ExtensionView } from '~/lib/extensions/types'

export interface FilterMenu {
  key: string
  title: string
  options: { value: string, label: string }[]
}

const props = defineProps<{
  type: ExtensionType
  view: ExtensionView
  filters: FilterMenu[]
  filterValues: Record<string, string>
  sortOptions: { value: string, label: string }[]
  addItems: (RowMenuItem | null)[]
}>()
const emit = defineEmits<{ 'update:type': [ExtensionType], 'update:view': [ExtensionView], 'filter': [string, string] }>()
const query = defineModel<string>('query', { default: '' })
const sort = defineModel<string>('sort', { default: '' })

const TYPES: { value: ExtensionType, label: string }[] = [{ value: 'skills', label: tt('Skills') }, { value: 'connectors', label: tt('Connectors') }, { value: 'plugins', label: tt('Plugins') }]
const VIEWS: { value: ExtensionView, label: string }[] = [{ value: 'yours', label: tt('Yours') }, { value: 'discover', label: tt('Discover') }]

const active = computed(() => props.filters.filter(f => props.filterValues[f.key]).length)
</script>

<template>
  <div
    data-pinned-row
    class="sticky top-0 z-20 border-b border-default bg-default"
  >
    <div class="mx-auto flex w-full max-w-[1040px] flex-wrap items-center gap-x-2 gap-y-2 px-10 py-2.5 max-sm:px-4">
      <div
        role="tablist"
        :aria-label="tt('What to show')"
        class="flex items-center"
      >
        <button
          v-for="t in TYPES"
          :key="t.value"
          type="button"
          role="tab"
          :aria-selected="type === t.value"
          class="cursor-pointer px-2.5 py-1 text-[12.5px]"
          :class="type === t.value ? 'bg-raised text-strong' : 'text-muted hover:text-strong'"
          @click="emit('update:type', t.value)"
        >
          {{ t.label }}
        </button>
      </div>
      <span class="mx-1 h-4 w-px bg-line max-sm:hidden" />
      <div
        role="group"
        :aria-label="tt('Yours or Discover')"
        class="flex items-center border border-default"
      >
        <button
          v-for="v in VIEWS"
          :key="v.value"
          type="button"
          :aria-pressed="view === v.value"
          class="cursor-pointer px-2.5 py-[3px] text-[12.5px]"
          :class="view === v.value ? 'bg-raised text-strong' : 'text-muted hover:text-strong'"
          @click="emit('update:view', v.value)"
        >
          {{ v.label }}
        </button>
      </div>

      <div class="ml-auto flex min-w-0 items-center gap-1.5 max-md:ml-0 max-md:w-full">
        <label class="flex h-7 w-[260px] min-w-0 items-center gap-1.5 border border-default px-2 focus-within:border-accent max-md:w-auto max-md:flex-1">
          <HIcon
            name="search"
            :size="13"
            class="shrink-0 text-muted"
          />
          <input
            v-model="query"
            type="search"
            :aria-label="tt('Search')"
            :placeholder="tt('Search')"
            spellcheck="false"
            class="min-w-0 flex-1 border-0 bg-transparent text-[12.5px] text-strong outline-none"
          >
        </label>

        <UPopover
          v-if="filters.length"
          :content="{ side: 'bottom', align: 'end', sideOffset: 4 }"
        >
          <button
            type="button"
            :aria-label="tt('Filter')"
            class="relative flex size-7 cursor-pointer items-center justify-center text-muted hover:text-strong"
            :class="active > 0 && 'text-accent'"
          >
            <HIcon
              name="filter"
              :size="15"
            />
            <span
              v-if="active"
              class="absolute top-0.5 right-0.5 size-1.5 bg-accent"
            />
          </button>
          <template #content>
            <div
              role="menu"
              :aria-label="tt('Filter')"
              class="min-w-[200px] border border-accented bg-hover py-1 text-[12.5px]"
            >
              <template
                v-for="(f, i) in filters"
                :key="f.key"
              >
                <div
                  v-if="i"
                  class="my-1 h-px bg-line"
                />
                <div class="px-[11px] py-1 text-[11px] text-muted">
                  {{ f.title }}
                </div>
                <PaneMenuItem
                  v-for="o in f.options"
                  :key="o.value"
                  role="menuitemradio"
                  :checked="filterValues[f.key] === o.value"
                  reserve
                  @click="emit('filter', f.key, o.value)"
                >
                  {{ o.label }}
                </PaneMenuItem>
              </template>
            </div>
          </template>
        </UPopover>

        <UPopover
          v-if="sortOptions.length"
          :content="{ side: 'bottom', align: 'end', sideOffset: 4 }"
        >
          <button
            type="button"
            :aria-label="tt('Sort')"
            class="flex size-7 cursor-pointer items-center justify-center text-muted hover:text-strong"
            :class="sort && 'text-accent'"
          >
            <HIcon
              name="list"
              :size="15"
            />
          </button>
          <template #content>
            <div
              role="menu"
              :aria-label="tt('Sort')"
              class="min-w-[180px] border border-accented bg-hover py-1 text-[12.5px]"
            >
              <PaneMenuItem
                v-for="o in sortOptions"
                :key="o.value"
                role="menuitemradio"
                :checked="sort === o.value"
                reserve
                @click="sort = o.value"
              >
                {{ o.label }}
              </PaneMenuItem>
            </div>
          </template>
        </UPopover>

        <RowMenu
          :items="addItems"
          :label="tt('Add')"
          align="end"
        >
          <HBtn
            variant="primary"
            size="md"
            :aria-label="tt('Add')"
          >
            <HIcon
              name="plus"
              :size="13"
            />
            {{ tt('Add') }}
            <HIcon
              name="down"
              :size="11"
            />
          </HBtn>
        </RowMenu>
      </div>
    </div>
  </div>
</template>
