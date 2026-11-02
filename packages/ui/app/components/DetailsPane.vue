<script setup lang="ts">
// Session details (the right panel of Main.dc.html): environment, context, providers, outputs, changes,
// agents and background tasks of the current session.
import type { SessionState, ToolItem } from '~/lib/session/types'
import { isSubagent } from '~/lib/session/reducer'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const now = useNow()
const relay = ref(true)

const model = computed(() => store.models.find(m => m.id === props.session.meta.model))
const contextWindow = computed(() => model.value?.contextWindow || 200_000)
const contextPct = computed(() => Math.min(100, Math.round((props.session.contextTokens / contextWindow.value) * 100)))

const providers = computed(() => {
  const seen = new Map<string, { name: string, offline: boolean }>()
  for (const m of store.models) {
    if (!m.provider || seen.has(m.provider)) continue
    seen.set(m.provider, { name: m.provider, offline: !!m.offline })
  }
  const active = model.value?.provider
  return [...seen.values()].map(p => ({
    ...p,
    letter: p.name.startsWith('Ollama') ? 'L' : p.name[0]!,
    state: p.name === active ? 'active' : p.offline ? 'connect' : 'ready'
  }))
})

const tools = computed(() => props.session.items.filter((i): i is ToolItem => i.kind === 'tool'))
const outputs = computed(() => {
  const files = new Set<string>()
  for (const t of tools.value) {
    if ((t.name === 'Write' || t.name === 'Edit') && typeof t.input.file_path === 'string') files.add(basename(t.input.file_path))
  }
  return [...files].slice(-3)
})
const agents = computed(() => tools.value.filter(isSubagent))
const runningAgents = computed(() => agents.value.filter(a => a.status === 'running'))
const tasks = computed(() => props.session.taskOrder.map(id => props.session.tasks[id]!))
const runningTasks = computed(() => tasks.value.filter(t => t.state === 'running'))
const repoName = computed(() => basename(props.session.meta.repo || props.session.meta.cwd))
const cwdParent = computed(() => props.session.meta.cwd.replace(/\/[^/]+$/, ''))

function age(ms: number) {
  return `${Math.max(1, Math.round(ms / 60_000))}m`
}

function taskMeta(command: string, tail: string) {
  const port = tail.match(/localhost:(\d+)/)?.[1]
  return port ? { text: `:${port}`, cls: 'text-ok' } : null
}
</script>

<template>
  <aside
    aria-label="Session details"
    class="flex h-full flex-col overflow-y-auto bg-default"
  >
    <section class="flex flex-col border-b border-default px-[13px] py-3">
      <span class="pb-1 text-[12px] text-muted">Environment</span>
      <div class="nav-row">
        <HIcon name="monitor" />local · {{ session.meta.deviceName || 'this machine' }}
      </div>
      <div class="nav-row">
        <HIcon name="folder" />{{ repoName }}<span class="text-[11px] text-muted">· {{ cwdParent }}</span>
      </div>
      <div class="nav-row">
        <HIcon name="branch" />{{ session.meta.gitBranch || 'main' }}
      </div>
      <div class="nav-row">
        <HIcon name="relay" />Session relay<span class="flex-1" /><HToggle
          v-model="relay"
          label="Session relay"
        />
      </div>
    </section>

    <section class="flex flex-col gap-[7px] border-b border-default px-[13px] py-3">
      <div class="flex items-center">
        <span class="text-[12px] text-muted">Context</span><span class="flex-1" /><span class="text-[12px] text-white">{{ contextPct }}%</span>
      </div>
      <div class="flex h-1 bg-raised">
        <span
          class="bg-accent"
          :style="{ width: `${contextPct}%` }"
        />
      </div>
      <div class="flex justify-between text-[10.5px] text-muted">
        <span>{{ formatTokens(session.contextTokens) }} / {{ formatTokens(contextWindow) }} tok</span><span>{{ formatCost(session.costUsd) }}</span>
      </div>
    </section>

    <section class="flex flex-col border-b border-default px-[13px] py-3">
      <span class="pb-1 text-[12px] text-muted">Providers</span>
      <div
        v-for="p in providers"
        :key="p.name"
        class="nav-row"
      >
        <span class="flex size-4 items-center justify-center border border-default text-[9.5px]">{{ p.letter }}</span>{{ p.name }}
        <span class="flex-1" />
        <span
          v-if="p.state === 'active'"
          class="text-[10.5px] text-ok"
        >active</span>
        <span
          v-else-if="p.state === 'ready'"
          class="text-[10.5px] text-muted"
        >ready</span>
        <a
          v-else
          href="#"
          class="text-[10.5px] text-orange"
        >Connect</a>
      </div>
      <a
        href="#"
        class="pt-0.5 text-[11px] text-muted hover:text-highlighted"
      >Show more</a>
    </section>

    <section
      v-if="outputs.length"
      class="flex flex-col border-b border-default px-[13px] py-3"
    >
      <span class="pb-1 text-[12px] text-muted">Outputs</span>
      <a
        v-for="f in outputs"
        :key="f"
        href="#"
        class="nav-row hover:bg-hover"
      ><HIcon name="file" />{{ f }}</a>
    </section>

    <section class="flex flex-col gap-[7px] border-b border-default px-[13px] py-3">
      <span class="text-[12px] text-muted">Changes</span>
      <div class="flex items-center gap-[9px] text-[12.5px]">
        <HIcon name="pull-request" />
        <span class="text-ok">+{{ formatCount(session.meta.additions) }}</span><span class="text-bad">−{{ formatCount(session.meta.deletions) }}</span>
        <span class="flex-1" />
        <button
          type="button"
          class="border border-default px-2.5 py-1 text-[11.5px] text-white hover:bg-hover"
        >
          {{ session.meta.pullRequest ? `PR ${session.meta.pullRequest.number}` : 'Create PR' }}
        </button>
      </div>
    </section>

    <section class="flex flex-col border-b border-default px-[13px] py-3">
      <div class="flex items-center pb-1">
        <span class="text-[12px] text-muted">Agents</span><span class="flex-1" /><span class="text-[10.5px] text-muted">{{ runningAgents.length }} running</span>
      </div>
      <div
        v-for="a in runningAgents"
        :key="a.id"
        class="nav-row"
      >
        <HIcon
          name="agent"
          class="text-accent"
        /><span class="truncate">{{ String(a.input.description ?? a.summary) }}</span>
        <span class="flex-1" /><span class="text-[10.5px] text-muted">{{ age(now - a.startedAt) }}</span>
      </div>
      <div class="nav-row text-muted">
        <HIcon name="check" />{{ agents.length - runningAgents.length }} finished
      </div>
    </section>

    <section class="flex flex-col px-[13px] py-3">
      <div class="flex items-center pb-1">
        <span class="text-[12px] text-muted">Background tasks</span><span class="flex-1" /><span class="text-[10.5px] text-muted">{{ runningTasks.length }} running</span>
      </div>
      <div
        v-for="t in runningTasks"
        :key="t.id"
        class="nav-row"
      >
        <HIcon name="term" /><span class="truncate">{{ t.command }}</span>
        <span class="flex-1" />
        <span
          v-if="taskMeta(t.command, t.tail)"
          class="text-[10.5px]"
          :class="taskMeta(t.command, t.tail)!.cls"
        >{{ taskMeta(t.command, t.tail)!.text }}</span>
        <span
          v-else
          class="text-[10.5px] text-muted"
        >{{ formatDuration(now - t.startedAt) }}</span>
      </div>
      <div class="nav-row text-muted">
        <HIcon name="check" />{{ tasks.length - runningTasks.length }} finished
      </div>
    </section>
  </aside>
</template>
