<script setup lang="ts">
// Todo list (the "Plan" box of Main.dc.html): done items struck through, the active one spinning in accent.
import type { TodoVM } from '~/lib/session/types'

defineProps<{ todos: TodoVM[] }>()
</script>

<template>
  <div class="flex flex-col gap-1.5 border border-l-[3px] border-default border-l-accent bg-surface px-[13px] py-2.5">
    <span class="text-[12px] text-muted">Plan</span>
    <div
      v-for="(t, i) in todos"
      :key="i"
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
  </div>
</template>
