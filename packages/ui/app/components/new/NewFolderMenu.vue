<script setup lang="ts">
// Folder picker of the new-session screen: search, recent folders, "Open folder…" (directory browser) and,
// for a repository, the branch a worktree starts from.
import type { Branch, RecentFolder } from '~/gen/harness/agent/v1/agent_pb'

const props = defineProps<{
  folders: RecentFolder[]
  cwd: string
  branches: Branch[]
  current: string
  baseRef: string
  worktree: boolean
}>()
const emit = defineEmits<{ select: [path: string], browse: [], branch: [name: string] }>()

const query = ref('')
const branchOpen = ref(false)
const shown = computed(() => {
  const q = query.value.toLowerCase()
  return props.folders.filter(f => !q || f.name.toLowerCase().includes(q) || f.path.toLowerCase().includes(q))
})
const ROW = 'flex cursor-pointer items-center gap-2 border-l-2 px-2.5 py-[5px]'
</script>

<template>
  <div
    role="menu"
    aria-label="Folder"
    class="w-[300px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
  >
    <label class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-muted">
      <HIcon
        name="search"
        :size="13"
      />
      <input
        v-model="query"
        placeholder="Search folders"
        class="min-w-0 flex-1 bg-transparent text-strong outline-none placeholder:text-muted"
        autofocus
      >
    </label>
    <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
      Recent
    </div>
    <div class="max-h-[260px] overflow-y-auto">
      <div
        v-for="f in shown"
        :key="f.path"
        role="menuitemradio"
        :aria-checked="f.path === cwd"
        :title="f.path"
        :class="[ROW, f.path === cwd ? 'border-accent bg-raised text-strong' : 'border-transparent text-toned hover:bg-raised hover:text-strong', !f.exists && 'opacity-50']"
        @click="f.exists && emit('select', f.path)"
      >
        <span class="min-w-0 flex-1 truncate">{{ f.name }}</span>
        <span
          v-if="f.gitBranch"
          class="shrink-0 text-[11px] text-muted"
        >{{ f.gitBranch }}</span>
        <HIcon
          v-if="f.path === cwd"
          name="check"
          :size="13"
          class="shrink-0 text-accent"
        />
      </div>
      <div
        v-if="!shown.length"
        class="px-2.5 py-1.5 text-muted"
      >
        {{ folders.length ? 'No matches' : 'No recent folders yet' }}
      </div>
    </div>
    <div class="my-1 h-px bg-line" />
    <div
      role="menuitem"
      :class="[ROW, 'border-transparent text-toned hover:bg-raised hover:text-strong']"
      @click="emit('browse')"
    >
      <HIcon
        name="folder"
        :size="13"
      />Open folder…
    </div>
    <div class="my-1 h-px bg-line" />
    <UPopover
      v-model:open="branchOpen"
      :content="{ side: 'right', align: 'end', sideOffset: 4 }"
      :disabled="!worktree || !branches.length"
    >
      <div
        role="menuitem"
        :aria-disabled="!worktree || !branches.length || undefined"
        :title="worktree ? 'Branch the worktree starts from' : 'Turn on Worktree to start from another branch'"
        :class="[ROW, 'border-transparent', worktree && branches.length ? 'text-toned hover:bg-raised hover:text-strong' : 'text-muted']"
      >
        <HIcon
          name="branch"
          :size="13"
        />
        <span class="flex-1">Branch</span>
        <span class="text-[11px] text-muted">{{ (worktree && baseRef) || current || '—' }}</span>
        <HIcon
          name="right"
          :size="12"
          class="text-muted"
        />
      </div>
      <template #content>
        <div
          role="menu"
          aria-label="Branch"
          class="max-h-[300px] w-[240px] overflow-y-auto border border-accented bg-hover text-[12.5px]"
        >
          <div
            v-for="b in branches"
            :key="b.name"
            role="menuitemradio"
            :aria-checked="b.name === (baseRef || current)"
            :class="[ROW, b.name === (baseRef || current) ? 'border-accent bg-raised text-strong' : 'border-transparent text-toned hover:bg-raised hover:text-strong']"
            @click="emit('branch', b.name); branchOpen = false"
          >
            <span class="min-w-0 flex-1 truncate">{{ b.name }}</span>
            <span
              v-if="b.name === current"
              class="text-[11px] text-muted"
            >current</span>
          </div>
        </div>
      </template>
    </UPopover>
  </div>
</template>
