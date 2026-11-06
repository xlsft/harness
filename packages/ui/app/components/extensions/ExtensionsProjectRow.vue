<script setup lang="ts">
// "In this project": the folder project skills, plugins and repository servers are told for. With none chosen, the row that chooses one.
import type { ExtensionType } from '~/lib/extensions/types'

const props = defineProps<{ cwd: string, trusted: boolean, what: ExtensionType, waiting?: boolean }>()
const emit = defineEmits<{ open: [], trust: [], close: [] }>()
const noun = computed(() => ({ skills: 'skills', plugins: 'plugins', connectors: 'connectors' }[props.what]))
</script>

<template>
  <div
    class="flex flex-col gap-1.5"
    data-project-row
  >
    <h2 class="m-0 text-[13px] font-medium text-strong">
      In this project
    </h2>
    <button
      v-if="!cwd"
      type="button"
      class="flex w-full cursor-pointer items-center gap-3 bg-raised px-3 py-2.5 text-left hover:bg-hover"
      @click="emit('open')"
    >
      <span class="flex size-8 shrink-0 items-center justify-center border border-default bg-default text-muted">
        <HIcon
          name="folder"
          :size="15"
        />
      </span>
      <span class="flex min-w-0 flex-col gap-0.5">
        <span class="text-[12.5px] text-strong">Open folder…</span>
        <span class="truncate text-[12px] text-muted">Choose the folder you work in to see its {{ noun }}.</span>
      </span>
    </button>
    <div
      v-else
      class="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-default pb-2 text-[12px]"
    >
      <HIcon
        name="folder"
        :size="14"
        class="shrink-0 text-muted"
      />
      <span
        class="min-w-0 flex-1 truncate text-toned"
        :title="cwd"
      >{{ cwd }}</span>
      <span
        v-if="!trusted"
        class="text-warn"
      >{{ waiting ? `Not trusted: its ${noun} wait for that` : 'Not trusted' }}</span>
      <HBtn
        v-if="!trusted"
        size="xs"
        @click="emit('trust')"
      >
        Trust this folder
      </HBtn>
      <HBtn
        size="xs"
        tone="muted"
        @click="emit('open')"
      >
        Change
      </HBtn>
      <HBtn
        size="xs"
        tone="muted"
        @click="emit('close')"
      >
        Close
      </HBtn>
    </div>
  </div>
</template>
