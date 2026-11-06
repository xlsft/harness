<script setup lang="ts">
// ReportFindings: the session's list as the client ordered it (most severe first). A row expands to the summary and the
// failure scenario; its location opens the file at the line in the Files pane.
import type { ToolItem } from '~/lib/session/types'
import { findingsOf, type Finding } from '~/lib/session/userTools'

const props = defineProps<{ tool: ToolItem, sessionId: string }>()
const { show } = useShow()

const findings = computed(() => findingsOf(props.tool) ?? [])
const level = computed(() => String(props.tool.result?.level ?? ''))
const open = computed(() => findings.value.filter(f => f.outcome === 'open').length)
const expanded = ref<Record<string, boolean>>({})

const SEVERITY_CLASS: Record<Finding['severity'], string> = {
  critical: 'text-bad',
  high: 'text-warn',
  medium: 'text-strong',
  low: 'text-muted'
}
const OUTCOME_LABEL: Record<string, string> = { fixed: 'fixed', wont_fix: 'won\'t fix', deferred: 'deferred' }

function location(f: Finding) {
  return f.line ? `${f.file}:${f.line}` : f.file
}
function openFile(f: Finding) {
  show({ kind: 'file', path: f.path, lineStart: f.line, lineEnd: 0 })
}
</script>

<template>
  <div class="border border-default bg-surface">
    <div class="flex items-center gap-2 px-3 py-2 text-[12.5px]">
      <HIcon
        :name="findings.length ? 'search' : 'check'"
        :size="14"
        class="shrink-0"
        :class="findings.length ? 'text-muted' : 'text-ok'"
      />
      <span class="min-w-0 flex-1 text-strong">
        <template v-if="findings.length">
          {{ tt('{0} {1}, {2} open', [findings.length, findings.length === 1 ? 'finding' : 'findings', open]) }}
        </template>
        <template v-else>{{ tt('No findings') }}</template>
      </span>
      <span
        v-if="level"
        class="shrink-0 text-[11.5px] text-muted"
      >{{ tt('{0} review', [level]) }}</span>
    </div>
    <div
      v-for="f in findings"
      :key="f.id"
      class="border-t border-default text-[12.5px]"
      :class="f.outcome !== 'open' && 'opacity-60'"
    >
      <div class="flex items-center gap-2 px-3 py-[7px]">
        <span
          class="w-12 shrink-0 text-[11.5px]"
          :class="SEVERITY_CLASS[f.severity]"
        >{{ f.severity }}</span>
        <button
          type="button"
          class="min-w-0 flex-1 truncate text-left text-strong"
          :aria-expanded="!!expanded[f.id]"
          @click="expanded[f.id] = !expanded[f.id]"
        >
          {{ f.short_summary || f.summary }}
        </button>
        <span
          v-if="OUTCOME_LABEL[f.outcome]"
          class="shrink-0 text-[11.5px] text-muted"
        >{{ OUTCOME_LABEL[f.outcome] }}</span>
        <button
          type="button"
          class="max-w-[40%] shrink-0 truncate font-mono text-[12px] text-muted hover:text-strong"
          :title="f.path"
          @click="openFile(f)"
        >
          {{ location(f) }}
        </button>
      </div>
      <div
        v-if="expanded[f.id]"
        class="flex flex-col gap-1.5 px-3 pb-2.5 leading-[1.55] text-muted"
      >
        <p class="m-0 whitespace-pre-wrap text-strong">
          {{ f.summary }}
        </p>
        <p class="m-0 whitespace-pre-wrap">
          {{ f.failure_scenario }}
        </p>
        <p
          v-if="f.category || f.verdict"
          class="m-0 text-[11.5px]"
        >
          {{ [f.category, f.verdict].filter(Boolean).join(' · ') }}
        </p>
      </div>
    </div>
  </div>
</template>
