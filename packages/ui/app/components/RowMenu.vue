<script setup lang="ts">
// A row (or button) that opens a small menu of actions under it: the folder, branch and relay menus of session
// details. Items run on click and close the menu; `null` items are separators; failures show a toast.
export interface RowMenuItem {
  label: string
  run: () => unknown
  disabled?: boolean
}

const props = defineProps<{ items: (RowMenuItem | null)[], label: string, align?: 'start' | 'end' }>()
const open = ref(false)
const toast = useToast()

// Separators only between visible items, never first or last.
const shown = computed(() => props.items.filter((it, i, all) => it || (i > 0 && all[i - 1] && all.slice(i + 1).some(Boolean))))

async function run(it: RowMenuItem) {
  open.value = false
  try {
    await it.run()
  } catch (e) {
    toast.add({ title: tt('Could not {0}', [it.label.replace(/…$/, '').toLowerCase()]), description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
  }
}
</script>

<template>
  <UPopover
    v-model:open="open"
    :content="{ side: 'bottom', align: align ?? 'start', sideOffset: 4 }"
  >
    <slot :open="open" />
    <template #content>
      <div
        role="menu"
        :aria-label="label"
        class="min-w-[200px] border border-accented bg-hover py-1 text-[12.5px]"
      >
        <template
          v-for="(it, i) in shown"
          :key="i"
        >
          <div
            v-if="!it"
            class="my-1 h-px bg-line"
          />
          <PaneMenuItem
            v-else
            :disabled="it.disabled"
            @click="run(it)"
          >
            {{ it.label }}
          </PaneMenuItem>
        </template>
      </div>
    </template>
  </UPopover>
</template>
