<script setup lang="ts">
// Internal references (Ref.dc.html): file chip (path:line), artifact, another session, commit and pull request.
const props = withDefaults(defineProps<{
  kind?: 'file' | 'artifact' | 'session' | 'commit' | 'pr'
  label: string
  // file: ":42" · artifact: "Design · 9 versions · 2 min ago" · session: "session · harness · feat/client-loop" · pr: "xlsft/harness · PR 14"
  detail?: string
  status?: string
  add?: number
  del?: number
  sha?: string
  branch?: string
  checks?: string
  tone?: 'accent' | 'ok'
}>(), { kind: 'file', detail: '', status: '', add: 0, del: 0, sha: '', branch: '', checks: '', tone: 'accent' })

const { setPane } = usePanes()
const fileName = computed(() => props.label)
</script>

<template>
  <a
    v-if="kind === 'file'"
    href="#"
    class="border border-default bg-surface px-1.5 py-px text-[12px] whitespace-nowrap text-accent hover:bg-hover"
    @click.prevent="setPane('files')"
  >{{ fileName }}<span
    v-if="detail"
    class="text-muted"
  >{{ detail }}</span></a>

  <a
    v-else-if="kind === 'artifact'"
    href="#"
    class="flex w-[480px] max-w-full items-center gap-3 border border-default bg-surface px-3 py-2.5 text-toned hover:bg-hover"
    @click.prevent="setPane('artifact')"
  >
    <span class="flex size-10 shrink-0 items-center justify-center border border-default bg-hover">
      <HIcon
        :name="tone === 'ok' ? 'file' : 'sidebar'"
        :size="18"
        :class="tone === 'ok' ? 'text-ok' : 'text-accent'"
      />
    </span>
    <span class="flex min-w-0 flex-1 flex-col"><span class="text-white">{{ label }}</span><span class="text-[11px] text-muted">{{ detail }}</span></span>
    <span class="flex items-center gap-1.5 border border-default px-[9px] py-[3px] text-[11.5px] text-white">Open<HIcon
      v-if="tone !== 'ok'"
      name="arrow-up-right"
      :size="12"
    /></span>
  </a>

  <a
    v-else-if="kind === 'session'"
    href="#"
    class="flex w-[480px] max-w-full items-center gap-3 border border-default bg-surface px-3 py-2.5 text-toned hover:bg-hover"
  >
    <HIcon
      name="session"
      :size="16"
      class="text-accent"
    />
    <span class="flex min-w-0 flex-1 flex-col"><span class="text-white">{{ label }}</span><span class="text-[11px] text-muted">{{ detail }}</span></span>
    <span
      v-if="status"
      class="text-[11px] text-accent"
    >{{ status }}</span><span
      v-if="add"
      class="text-[11px] text-ok"
    >+{{ add }}</span>
  </a>

  <div
    v-else-if="kind === 'commit'"
    class="flex w-[480px] max-w-full items-center gap-2.5 border border-default bg-surface px-3 py-2 text-[12.5px]"
  >
    <HIcon
      name="commit"
      :size="16"
      class="text-muted"
    />
    <a
      href="#"
      class="link"
    >{{ sha }}</a>
    <span class="min-w-0 flex-1 truncate text-white">{{ label }}</span>
    <span class="text-[11px] text-ok">+{{ add }}</span><span class="text-[11px] text-bad">−{{ del }}</span>
  </div>

  <a
    v-else
    href="#"
    class="flex w-[480px] max-w-full flex-col gap-2 border border-l-2 border-default border-l-ok bg-surface px-3 py-2.5 text-toned"
  >
    <span class="flex items-center gap-[9px]"><HIcon
      name="pull-request"
      :size="16"
      class="text-ok"
    /><span class="text-white">{{ label }}</span></span>
    <span class="flex flex-wrap gap-2.5 text-[11px] text-muted">
      <span>{{ detail }}</span><span>{{ branch }}</span><span class="text-ok">{{ checks }}</span><span class="text-ok">+{{ add }}</span><span class="text-bad">−{{ del }}</span>
    </span>
  </a>
</template>
