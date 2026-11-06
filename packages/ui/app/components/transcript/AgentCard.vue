<script setup lang="ts">
// Subagent (Agent.dc.html): running with its recent tool calls, or done with its report.
import { usePanes } from '~/composables/usePanes'
import type { ToolItem } from '~/lib/session/types'

const props = defineProps<{ tool: ToolItem, sessionId: string }>()
const store = useSessionStore()
const { openPane } = usePanes()
const now = useNow()

// The Agent call ends at once; from then on the agent it started says how things stand.
const agent = computed(() => props.tool.subagent?.agentId ? store.sessions[props.sessionId]?.subagents[props.tool.subagent.agentId] : undefined)
const running = computed(() => agent.value ? agent.value.state === 'running' || agent.value.state === 'queued' : props.tool.status === 'running')
const failed = computed(() => agent.value ? agent.value.state !== 'finished' && !running.value : props.tool.status === 'error')
const stateLabel = computed(() => agent.value && agent.value.state !== 'finished' ? agent.value.state : running.value ? 'running' : failed.value ? 'failed' : 'done')
const report = computed(() => agent.value ? agent.value.report || agent.value.error : props.tool.output)
const canStop = computed(() => {
  const meta = store.sessions[props.sessionId]?.meta
  return !meta?.remote || meta.remoteControllable
})
const type = computed(() => String(props.tool.input.subagent_type ?? 'general-purpose'))
const sub = computed(() => props.tool.subagent)
const elapsed = computed(() => {
  if (agent.value) return formatDuration((agent.value.finishedAt || now.value) - agent.value.startedAt)
  return formatDuration(running.value ? now.value - props.tool.startedAt : props.tool.durationMs)
})
const footer = computed(() => {
  const parts = [`${sub.value?.toolCalls ?? 0} tool calls`]
  if (sub.value?.tokens) parts.push(`${formatTokens(sub.value.tokens)} tok`)
  return parts.join(' · ')
})
</script>

<template>
  <div class="border border-default">
    <div class="flex flex-wrap items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        :name="running ? 'agent' : failed ? 'x' : 'check'"
        :class="running ? 'text-accent' : failed ? 'text-bad' : 'text-ok'"
      />
      <span class="text-strong">{{ tt('Agent') }}</span>
      <span class="border border-default px-[5px] text-[11px] text-muted">{{ type }}</span>
      <span class="text-muted">{{ toolTarget(tool) }}</span>
      <span class="flex-1" />
      <span
        v-if="running && sub?.model"
        class="text-[11px] text-muted"
      >{{ sub.model }}</span>
      <span
        class="text-[11px]"
        :class="running ? 'text-accent' : failed ? 'text-bad' : 'text-ok'"
      >{{ stateLabel }} · {{ elapsed }}</span>
    </div>

    <div
      v-if="running && sub?.steps.length"
      class="flex flex-col gap-0.5 border-l-2 border-accent bg-surface px-3 py-1.5 text-[12px] text-muted"
    >
      <div
        v-for="(s, i) in sub.steps"
        :key="i"
        class="flex items-center gap-2"
        :class="s.status === 'running' ? 'text-strong' : ''"
      >
        <span
          class="w-3"
          :class="s.status === 'running' ? 'text-accent' : s.status === 'failed' ? 'text-bad' : 'text-ok'"
        >{{ s.status === 'running' ? '›' : s.status === 'failed' ? '✕' : '✓' }}</span>
        {{ s.tool }}<span :class="s.status === 'running' ? '' : 'text-toned'">{{ s.target }}</span>
        <span class="flex-1" />{{ s.meta }}
      </div>
    </div>

    <div
      v-else-if="!running && report"
      class="mx-3 my-2 border-l-2 border-default py-2.5 pr-3 pl-3.5 text-[12.5px] leading-[1.6]"
    >
      <MarkdownBlock :text="report" />
    </div>

    <div
      class="flex items-center gap-3 border-t border-default px-3 py-1.5 text-[11px] text-muted"
      :class="running && !sub?.steps.length ? 'border-t-0' : ''"
    >
      {{ footer }}
      <span class="flex-1" />
      <a
        v-if="agent"
        href="#"
        class="link"
        @click.prevent="openPane('agents')"
      >{{ tt('Open transcript') }}</a>
      <HBtn
        v-if="running && agent && canStop"
        @click="store.stopSubagent(sessionId, agent.id)"
      >
        {{ tt('Stop') }}
      </HBtn>
    </div>
  </div>
</template>
