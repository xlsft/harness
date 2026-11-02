<script setup lang="ts">
// Renders a pane layout tree: a split becomes a Reka UI SplitterGroup (row = side by side, col = stacked)
// with a resize handle between children; a leaf is a RightPane. Sizes are saved per group by Reka.
import { SplitterGroup, SplitterPanel } from 'reka-ui'
import type { SessionState } from '~/lib/session/types'
import { PANE_WIDTH } from '~/composables/usePanes'
import { nodeKey, panesOf, type LayoutNode } from '~/composables/usePaneLayout'

const props = defineProps<{ node: LayoutNode, session: SessionState }>()

// Default share of each child: by the panes' natural widths in a row, equal in a column.
function share(n: LayoutNode): number {
  if (props.node.kind !== 'split') return 100
  const weight = (c: LayoutNode) => props.node.kind === 'split' && props.node.dir === 'row'
    ? Math.max(...panesOf(c).map(p => PANE_WIDTH[p]))
    : 1
  const total = props.node.children.reduce((s, c) => s + weight(c), 0)
  return (weight(n) / total) * 100
}
</script>

<template>
  <RightPane
    v-if="node.kind === 'pane'"
    :pane="node.pane"
    :session="session"
  />
  <SplitterGroup
    v-else
    :direction="node.dir === 'row' ? 'horizontal' : 'vertical'"
    :auto-save-id="`harness-split-${session.meta.id}-${node.dir}-${nodeKey(node)}`"
    class="size-full"
  >
    <template
      v-for="(child, i) in node.children"
      :key="nodeKey(child)"
    >
      <HSplitHandle
        v-if="i > 0"
        :dir="node.dir"
      />
      <SplitterPanel
        :id="nodeKey(child)"
        :order="i"
        :default-size="share(child)"
        :min-size="10"
        class="min-h-0 min-w-0"
      >
        <PaneLayout
          :node="child"
          :session="session"
        />
      </SplitterPanel>
    </template>
  </SplitterGroup>
</template>
