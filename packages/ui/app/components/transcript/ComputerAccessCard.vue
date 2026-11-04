<script setup lang="ts">
// ComputerAccess: the model asks to use apps of this machine. Each app comes with the tier the client gave it, and the
// user approves them one by one. Grants last for the session. Resolved by ComputerAccessResolved; once resolved it
// collapses to one muted line.
import { TIER_LABELS } from '~/lib/session/computer'
import type { ComputerAccessItem } from '~/lib/session/types'

const props = defineProps<{ item: ComputerAccessItem, sessionId: string }>()
const store = useSessionStore()

const pending = computed(() => props.item.state === 'pending')
const remote = computed(() => !!store.sessions[props.sessionId]?.meta.remote)
const chosen = ref<Set<string>>(new Set(props.item.apps.map(a => a.id)))
const sending = ref(false)
const error = ref('')

function toggle(id: string, on: boolean) {
  const next = new Set(chosen.value)
  if (on) next.add(id)
  else next.delete(id)
  chosen.value = next
}

async function answer(ids: string[]) {
  if (!pending.value || sending.value) return
  sending.value = true
  error.value = ''
  try {
    await store.respondToComputerAccess(props.sessionId, props.item.id, ids)
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
  } finally {
    sending.value = false
  }
}

const names = computed(() => props.item.apps.map(a => a.name).join(', '))
const summary = computed(() => {
  if (props.item.state === 'cancelled') return `Access to ${names.value} was not answered`
  const approved = props.item.apps.filter(a => props.item.approved.includes(a.id)).map(a => a.name)
  return approved.length ? `Access granted to ${approved.join(', ')}` : `Access to ${names.value} denied`
})
</script>

<template>
  <div
    v-if="pending"
    class="border border-accent bg-surface"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        name="monitor"
        class="text-accent"
      />
      <span class="text-strong">Let the agent use these apps on this machine?</span>
      <span class="flex-1" />
      <span class="text-[11px] text-muted">waiting</span>
    </div>
    <div class="flex flex-col gap-2.5 px-3 py-2.5 text-[12.5px]">
      <span
        v-if="item.reason"
        class="text-toned"
      >{{ item.reason }}</span>
      <label
        v-for="a in item.apps"
        :key="a.id"
        class="flex cursor-pointer items-start gap-2"
      >
        <input
          type="checkbox"
          class="mt-[3px] accent-(--color-accent)"
          :checked="chosen.has(a.id)"
          @change="toggle(a.id, ($event.target as HTMLInputElement).checked)"
        >
        <span class="flex min-w-0 flex-col">
          <span class="flex flex-wrap items-baseline gap-x-2">
            <span class="text-strong">{{ a.name }}</span>
            <span class="text-[11.5px] text-muted">{{ TIER_LABELS[a.tier] }}</span>
          </span>
          <span class="text-[11.5px] text-muted">{{ a.note }}</span>
        </span>
      </label>
      <div
        v-for="p in item.missing"
        :key="p.id"
        class="border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12px] text-toned"
      >
        <span class="text-strong">{{ p.name }} is not allowed yet.</span> {{ p.howToGrant }}
      </div>
      <span class="text-[11.5px] text-muted">Lasts for this session. You can stop it at any time.</span>
      <p
        v-if="error"
        role="alert"
        class="m-0 text-[12px] text-bad"
      >
        {{ error }}
      </p>
      <span
        v-if="remote"
        class="text-[12px] text-muted"
      >Only someone at the machine running this session can answer.</span>
      <div
        v-else
        class="flex flex-wrap gap-2"
      >
        <HBtn
          variant="primary"
          size="lg"
          :disabled="sending || chosen.size === 0"
          @click="answer([...chosen])"
        >
          Allow {{ chosen.size === item.apps.length ? '' : 'selected' }}
        </HBtn>
        <HBtn
          size="lg"
          :disabled="sending"
          @click="answer([])"
        >
          Deny
        </HBtn>
      </div>
    </div>
  </div>
  <div
    v-else
    class="flex items-center gap-[9px] text-[12px] text-muted"
  >
    <HIcon
      name="monitor"
      :size="14"
      :class="item.approved.length ? 'text-ok' : 'text-bad'"
    />
    <span class="truncate">{{ summary }}</span>
  </div>
</template>
