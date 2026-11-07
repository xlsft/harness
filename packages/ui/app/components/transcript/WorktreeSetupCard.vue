<script setup lang="ts">
// The setup of a new worktree (WorktreeSetupUpdated; docs/design/worktree-setup.md): the steps of the project's
// .harness/worktree.json as the client runs them, or a one-time suggestion for a project with lockfiles and no setup.
// The client decides and runs; this card shows the steps and sends the answer to a suggestion.
import type { WorktreeSetupItem, WorktreeSetupStepVM } from '~/lib/session/types'

const props = defineProps<{ item: WorktreeSetupItem, sessionId: string }>()
const store = useSessionStore()
const busy = ref(false)
const failure = ref('')
// Output of a step: open while it runs and for the ones that failed.
const opened = ref<Record<number, boolean>>({})

const suggested = computed(() => props.item.state === 'suggested')
const title = computed(() => {
  switch (props.item.state) {
    case 'suggested': return tt('Set up worktrees for this project?')
    case 'running': return tt('Setting up the worktree')
    case 'succeeded': return tt('Worktree set up')
    case 'failed': return props.item.error ? tt('Worktree setup could not run') : tt('Worktree setup failed')
    case 'dismissed': return tt('Worktree setup suggestion dismissed')
  }
  return ''
})
const tone = computed(() => {
  switch (props.item.state) {
    case 'running': case 'suggested': return 'text-accent'
    case 'succeeded': return 'text-ok'
    case 'failed': return 'text-bad'
  }
  return 'text-muted'
})

function stepText(st: WorktreeSetupStepVM): { text: string, cls: string } {
  switch (st.status) {
    case 'running': return { text: tt('running'), cls: 'text-accent' }
    case 'ok': return { text: formatSeconds(st.durationMs), cls: 'text-ok' }
    case 'failed': return { text: st.exitCode >= 0 ? tt('exit {0} · {1}', [st.exitCode, formatSeconds(st.durationMs)]) : tt('could not run'), cls: 'text-bad' }
    case 'timed_out': return { text: tt('timed out · {0}', [formatSeconds(st.durationMs)]), cls: 'text-bad' }
    case 'canceled': return { text: tt('stopped'), cls: 'text-muted' }
  }
  return { text: suggested.value ? '' : tt('waiting'), cls: 'text-dimmed' }
}

function isOpen(i: number, st: WorktreeSetupStepVM) {
  const set = opened.value[i]
  if (set !== undefined) return set
  return st.status === 'running' || st.status === 'failed' || st.status === 'timed_out'
}

async function answer(accept: boolean) {
  busy.value = true
  failure.value = ''
  try {
    await store.answerWorktreeSetup(props.sessionId, props.item.id, accept)
  } catch (e) {
    failure.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div
    class="border border-default"
    :class="item.state === 'failed' ? 'border-l-2 border-l-bad' : suggested ? 'border-l-2 border-l-accent' : ''"
    data-worktree-setup
  >
    <div class="flex items-center gap-[9px] px-3 py-[7px] text-[12.5px] text-muted">
      <HIcon
        :name="item.state === 'running' ? 'loading' : 'box'"
        :spin="item.state === 'running'"
        :class="tone"
      />
      <span class="shrink-0 text-strong">{{ title }}</span>
      <span
        v-if="item.agentId"
        class="shrink-0 text-[11px]"
      >{{ tt('agent {0}', [item.agentId]) }}</span>
      <span
        class="min-w-0 truncate font-mono text-[11px] text-dimmed"
        :title="item.worktreePath"
      >{{ item.worktreePath }}</span>
      <span class="flex-1" />
      <span
        v-if="item.durationMs && !suggested"
        class="shrink-0 text-[11px]"
      >{{ formatSeconds(item.durationMs) }}</span>
    </div>

    <p
      v-if="suggested"
      class="border-t border-default px-3 py-[7px] text-[12px] text-muted"
    >
      {{ tt('This project has lockfiles but no worktree setup, so new worktrees start without dependencies. Accepting writes {0} and runs these installs now and in every new worktree. They use the package manager\'s own cache; nothing is linked to another checkout.', [item.configPath]) }}
    </p>
    <p
      v-if="item.error"
      class="border-t border-default px-3 py-[7px] text-[12px] text-bad"
    >
      {{ item.error }}
    </p>

    <ul
      v-if="item.steps.length && item.state !== 'dismissed'"
      class="border-t border-default"
    >
      <li
        v-for="(st, i) in item.steps"
        :key="i"
        class="border-b border-default last:border-b-0"
      >
        <button
          type="button"
          class="flex w-full items-center gap-[9px] px-3 py-[5px] text-left text-[12px]"
          :disabled="!st.output && !st.error"
          :aria-expanded="isOpen(i, st)"
          @click="opened[i] = !isOpen(i, st)"
        >
          <HIcon
            :name="st.status === 'ok' ? 'check' : st.status === 'running' ? 'loading' : st.status === 'pending' ? 'dot' : 'warn'"
            :spin="st.status === 'running'"
            :size="13"
            :class="stepText(st).cls"
          />
          <span class="min-w-0 truncate font-mono text-strong">{{ st.command }}</span>
          <span
            v-if="st.dir !== '.'"
            class="shrink-0 font-mono text-[11px] text-dimmed"
          >{{ tt('in {0}', [st.dir]) }}</span>
          <span class="flex-1" />
          <span
            class="shrink-0 text-[11px]"
            :class="stepText(st).cls"
          >{{ stepText(st).text }}</span>
        </button>
        <pre
          v-if="isOpen(i, st) && (st.output || st.error)"
          class="m-0 max-h-60 overflow-auto bg-surface px-3 py-[7px] text-[11.5px] leading-[1.6] text-toned"
        >{{ stripTerminalCodes(st.output) }}<span
          v-if="st.error"
          class="text-bad"
        >{{ st.error }}</span></pre>
      </li>
    </ul>

    <div
      v-if="suggested"
      class="flex flex-wrap items-center gap-2 border-t border-default px-3 py-[7px]"
    >
      <HBtn
        variant="primary"
        size="sm"
        :disabled="busy"
        @click="answer(true)"
      >
        {{ tt('Set up') }}
      </HBtn>
      <HBtn
        size="sm"
        tone="muted"
        :disabled="busy"
        @click="answer(false)"
      >
        {{ tt('Not for this project') }}
      </HBtn>
      <span
        v-if="failure"
        class="text-[12px] text-bad"
      >{{ failure }}</span>
    </div>
  </div>
</template>
