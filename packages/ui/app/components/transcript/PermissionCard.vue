<script setup lang="ts">
// Permission request (Tool.dc.html "permission"): accent-framed card with the command, the reason and
// Allow (1) / Always for <rule> (2) / Deny (esc). Keys 1, 2 and Esc answer while it is pending.
// Once resolved (PermissionResolved) it collapses to one muted line.
import type { PermissionItem } from '~/lib/session/types'

const props = defineProps<{ item: PermissionItem, sessionId: string }>()
const store = useSessionStore()

const command = computed(() => {
  const i = props.item.input
  return String(i.command ?? i.file_path ?? i.notebook_path ?? i.url ?? i.pattern ?? JSON.stringify(i))
})
const rule = computed(() => props.item.suggestedRules[0] ?? '')
const ruleLabel = computed(() => rule.value.replace(/^\w+\((.*)\)$/, '$1') || props.item.toolName)
const pending = computed(() => props.item.state === 'pending')

function answer(d: 'allow' | 'always' | 'deny') {
  if (!pending.value) return
  store.respondToPermission(props.sessionId, props.item.id, d, d === 'always' ? rule.value : '')
}

defineShortcuts(computed(() => pending.value
  ? { 1: () => answer('allow'), 2: () => answer('always'), escape: { usingInput: true, handler: () => answer('deny') } }
  : {}))

const resolved = computed(() => ({
  allowed: { text: 'Allowed', cls: 'text-ok' },
  allowed_always: { text: 'Always allowed', cls: 'text-ok' },
  denied: { text: 'Denied', cls: 'text-bad' },
  pending: { text: '', cls: '' }
}[props.item.state]))
</script>

<template>
  <div
    v-if="pending"
    class="border border-accent bg-surface"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        name="shield"
        class="text-accent"
      />
      <span class="text-strong">Allow {{ item.toolName }}?</span>
      <span class="flex-1" />
      <span class="text-[11px] text-muted">waiting</span>
    </div>
    <div class="flex flex-col gap-2.5 px-3 py-2.5 text-[12.5px]">
      <code class="block border border-default bg-default px-2.5 py-[7px] break-all text-strong">{{ command }}</code>
      <span
        v-if="item.reason"
        class="text-muted"
      >{{ item.reason }}</span>
      <div class="flex flex-wrap gap-2">
        <HBtn
          variant="primary"
          size="lg"
          @click="answer('allow')"
        >
          Allow <span class="opacity-70">1</span>
        </HBtn>
        <HBtn
          size="lg"
          @click="answer('always')"
        >
          Always for {{ ruleLabel }} <span class="text-muted">2</span>
        </HBtn>
        <HBtn
          size="lg"
          @click="answer('deny')"
        >
          Deny <span class="text-muted">esc</span>
        </HBtn>
      </div>
    </div>
  </div>
  <div
    v-else
    class="flex items-center gap-[9px] text-[12px] text-muted"
  >
    <HIcon
      name="shield"
      :size="14"
      :class="resolved.cls"
    />
    <span :class="resolved.cls">{{ resolved.text }}</span>{{ item.toolName }}
    <span class="truncate text-toned">{{ command }}</span>
    <span class="flex-1" />
    <span
      v-if="item.resolvedBy"
      class="shrink-0"
    >by {{ item.resolvedBy }}</span>
  </div>
</template>
