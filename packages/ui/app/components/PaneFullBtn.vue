<script setup lang="ts">
// The header button that opens this pane full screen. RightPane says which pane and session it is.
import type { ComputedRef } from 'vue'
import type { PaneName } from '~/composables/usePanes'

const injected = inject<ComputedRef<{ pane: PaneName, sessionId: string } | null> | null>('pane-full-target', null)
const target = computed(() => injected?.value ?? null)
const { isFull, enter } = usePaneFullscreen()
</script>

<template>
  <PaneBtn
    v-if="target && !isFull(target.sessionId, target.pane)"
    icon="full"
    :label="tt('Open full screen')"
    @click="enter(target.sessionId, target.pane)"
  />
</template>
