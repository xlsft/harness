<script setup lang="ts">
// Environment picker of the new-session screen: the connected client ("This computer"), or an always-on host from the
// server's list (docs/design/hosts.md). A host that can't take the work says why; an offline one still takes it, and the
// work waits. SSH hosts come later.
import { hostLoadText, type HostView } from '~/lib/hosts/types'

const props = defineProps<{ hosts?: HostView[], current?: string }>()
const emit = defineEmits<{ close: [], pick: [hostId: string] }>()
const query = ref('')
const ROW = 'flex cursor-pointer items-center gap-2 border-l-2 px-2.5 py-[5px]'

const shown = computed(() => {
  const q = query.value.trim().toLowerCase()
  return (props.hosts ?? []).filter(h => !q || h.name.toLowerCase().includes(q) || h.groupName.toLowerCase().includes(q))
})

function pick(id: string) {
  emit('pick', id)
  emit('close')
}
</script>

<template>
  <div
    role="menu"
    aria-label="Environment"
    class="w-[300px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
  >
    <label class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-muted">
      <HIcon
        name="search"
        :size="13"
      />
      <input
        v-model="query"
        placeholder="Search environments"
        class="min-w-0 flex-1 bg-transparent text-strong outline-none placeholder:text-muted"
      >
    </label>
    <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
      Local
    </div>
    <div
      role="menuitemradio"
      :aria-checked="!current"
      :class="[ROW, !current ? 'border-accent bg-raised text-strong' : 'border-transparent text-toned hover:bg-raised']"
      @click="pick('')"
    >
      <HIcon
        name="monitor"
        :size="13"
      />
      <span class="flex-1">This computer</span>
      <HIcon
        v-if="!current"
        name="check"
        :size="13"
        class="text-accent"
      />
    </div>
    <template v-if="shown.length">
      <div class="my-1 h-px bg-line" />
      <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
        Always-on hosts
      </div>
      <div
        v-for="h in shown"
        :key="h.id"
        role="menuitemradio"
        :aria-checked="current === h.id"
        :aria-disabled="!!h.unavailableReason"
        :title="h.unavailableReason || undefined"
        :class="[ROW, current === h.id ? 'border-accent bg-raised text-strong' : 'border-transparent', h.unavailableReason ? 'cursor-not-allowed text-muted' : 'text-toned hover:bg-raised']"
        @click="!h.unavailableReason && pick(h.id)"
      >
        <HIcon
          name="box"
          :size="13"
        />
        <span class="min-w-0 flex-1 truncate">{{ h.name }}<template v-if="h.groupName"> · {{ h.groupName }}</template></span>
        <span class="shrink-0 text-[11px] text-muted">{{ h.unavailableReason ? 'unavailable' : hostLoadText(h) }}</span>
        <HIcon
          v-if="current === h.id"
          name="check"
          :size="13"
          class="text-accent"
        />
      </div>
    </template>
    <div class="my-1 h-px bg-line" />
    <div
      role="menuitem"
      aria-disabled="true"
      :class="[ROW, 'border-transparent text-muted']"
    >
      <HIcon
        name="terminal"
        :size="13"
      />
      <span class="flex-1">SSH</span>
      <span class="text-[11px]">soon</span>
    </div>
  </div>
</template>
