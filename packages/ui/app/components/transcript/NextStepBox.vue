<script setup lang="ts">
// The box a turn closes with (the WrapUp tool), under the last message: a recap and, when the next move is the user's,
// one question with buttons. A button sends its label as the user's reply; the cross hides the box.
import type { ToolItem } from '~/lib/session/types'

const props = defineProps<{ tool: ToolItem, sessionId: string }>()
const emit = defineEmits<{ dismiss: [] }>()
const store = useSessionStore()
const recap = computed(() => String(props.tool.input.recap ?? ''))
const ask = computed(() => String(props.tool.input.ask ?? ''))
const actions = computed(() => Array.isArray(props.tool.input.actions) ? (props.tool.input.actions as unknown[]).map(String) : [])
function answer(label: string) {
  emit('dismiss')
  void store.send(props.sessionId, label)
}
</script>

<template>
  <div class="flex flex-col gap-2.5 border border-default px-3.5 py-3 text-[12.5px]">
    <div class="flex items-start gap-3">
      <p class="m-0 min-w-0 flex-1 leading-[1.55] text-muted">
        {{ recap }}
      </p>
      <HIconBtn
        icon="close"
        label="Hide"
        :size="26"
        :icon-size="12"
        class="-mt-1 -mr-1.5"
        @click="emit('dismiss')"
      />
    </div>
    <template v-if="ask">
      <p class="m-0 text-strong">
        {{ ask }}
      </p>
      <div class="flex flex-wrap gap-2">
        <HBtn
          v-for="(a, i) in actions"
          :key="a"
          size="md"
          :variant="i === 0 ? 'primary' : 'outline'"
          @click="answer(a)"
        >
          {{ a }}
        </HBtn>
      </div>
    </template>
  </div>
</template>
