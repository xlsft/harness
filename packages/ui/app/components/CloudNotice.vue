<script setup lang="ts">
// The container of a cloud session (docs/design/cloud-sessions.md), above the composer: being started, or gone until the
// next message. A running one says nothing. The server decides; this only words it.
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()

const notice = computed(() => {
  switch (props.session.meta.cloudEnvironment) {
    case 'starting': return { text: tt('Starting environment…'), busy: true }
    case 'removed': return { text: tt('The environment was removed while idle; your next message starts a new one'), busy: false }
    case 'failed': return { text: tt('The environment did not start; your next message tries again'), busy: false }
    case 'ended': return { text: tt('This cloud session has ended'), busy: false }
  }
  return null
})
</script>

<template>
  <div
    v-if="notice"
    role="status"
    class="flex items-center gap-2 border border-line bg-raised px-3 py-1.5 text-[12px] text-toned"
  >
    <HIcon
      :name="notice.busy ? 'loading' : 'globe'"
      :size="13"
      :spin="notice.busy"
    />
    <span class="min-w-0 flex-1">{{ notice.text }}</span>
  </div>
</template>
