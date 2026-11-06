<script setup lang="ts">
// Todo list (the "Plan" box of Main.dc.html): done items struck through, the active one spinning in accent, with a quiet
// line under it while agents or background tasks run for it.
import { usePanes } from '~/composables/usePanes'
import { todoWork, todoWorkLabel, type TodoWork } from '~/lib/session/todoWork'
import type { SubagentVM, TaskVM, TodoVM } from '~/lib/session/types'

const props = defineProps<{ todos: TodoVM[], subagents: Record<string, SubagentVM>, tasks: Record<string, TaskVM> }>()

const { openPane } = usePanes()

const rows = computed(() => props.todos.map((todo) => {
  const work = todoWork(todo, props.subagents, props.tasks)
  return { todo, work, line: todoWorkLabel(work) }
}))

// The panes that list this work: agents and background tasks each have their own.
function openWork(work: TodoWork) {
  if (work.agents) openPane('agents')
  if (work.tasks) openPane('tasks')
}
</script>

<template>
  <div class="flex flex-col gap-1.5 border border-l-[3px] border-default border-l-accent bg-surface px-[13px] py-2.5">
    <span class="text-[12px] text-muted">{{ tt('Plan') }}</span>
    <div
      v-for="({ todo: t, work, line }, i) in rows"
      :key="i"
      class="flex flex-col gap-0.5"
    >
      <div
        class="flex items-center gap-[9px] text-[12.5px]"
        :class="t.status === 'completed' ? 'text-muted' : t.status === 'in_progress' ? 'text-strong' : 'text-toned'"
      >
        <HIcon
          v-if="t.status === 'completed'"
          name="checkbox-checked"
          class="text-ok"
        />
        <HIcon
          v-else-if="t.status === 'in_progress'"
          name="loading"
          spin
          class="text-accent"
        />
        <HIcon
          v-else
          name="checkbox"
          class="text-muted"
        />
        <s v-if="t.status === 'completed'">{{ t.content }}</s>
        <template v-else>
          {{ t.content }}
        </template>
      </div>
      <!-- Indented to the item's text: the icon's 15px and the row's 9px gap. -->
      <button
        v-if="line"
        type="button"
        class="ml-6 w-fit text-[11.5px] text-muted hover:text-toned"
        @click="openWork(work)"
      >
        {{ line }}
      </button>
    </div>
  </div>
</template>
