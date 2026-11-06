<script setup lang="ts">
// Above the composer while the session has computer use: the indicator that the agent controls this machine, with an
// instant Stop (cancels the action in flight, revokes the grants, interrupts the turn). Between turns it is a muted line
// with the apps that still have access. The client also shows an indicator on the machine itself, since this window
// may be behind the app the agent works in.
import type { SessionState } from '~/lib/session/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const toast = useToast()

const state = computed(() => props.session.computerUse)
const shown = computed(() => !!state.value && (state.value.controlling || state.value.grants.length > 0))
const apps = computed(() => state.value?.grants.map(g => g.name).join(', ') ?? '')

async function stop() {
  try {
    await store.stopComputerUse(props.session.meta.id)
  } catch (e) {
    toast.add({ title: tt('Could not stop computer use'), description: e instanceof Error ? e.message : String(e), color: 'error' })
  }
}
</script>

<template>
  <div
    v-if="shown"
    class="flex items-center gap-[9px] border border-l-2 px-2.5 py-1.5 text-[12px]"
    :class="state!.controlling ? 'border-accent border-l-accent' : 'border-default'"
    role="status"
  >
    <HIcon
      name="monitor"
      :size="14"
      :class="state!.controlling ? 'text-accent' : 'text-muted'"
    />
    <span class="flex min-w-0 flex-1 flex-col">
      <span :class="state!.controlling ? 'text-strong' : 'text-muted'">
        {{ state!.controlling ? (state!.action || tt('The agent is controlling this machine')) : tt('Computer use has access') }}
      </span>
      <span
        v-if="apps"
        class="truncate text-[11.5px] text-muted"
      >{{ apps }}</span>
    </span>
    <HBtn
      :variant="state!.controlling ? 'primary' : undefined"
      @click="stop"
    >
      {{ state!.controlling ? tt('Stop') : tt('Revoke') }}
    </HBtn>
  </div>
</template>
