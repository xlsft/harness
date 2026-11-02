<script setup lang="ts">
// Edit diff (Diff.dc.html): unified or split, with the Unified / Split toggle in the header.
import type { PatchHunk } from '~/utils/tools'

const props = withDefaults(defineProps<{
  path: string
  hunks: PatchHunk[]
  verb?: string
  mode?: 'unified' | 'split'
  icon?: boolean
}>(), { verb: 'Edit', mode: 'unified', icon: true })

const view = ref(props.mode)
const stats = computed(() => patchStats(props.hunks))

const ADD = { bg: '#00bc7d1f', sc: '#00bc7d', tc: '#fff', s: '+' }
const DEL = { bg: '#ff20561f', sc: '#ff2056', tc: '#fff', s: '−' }
const CTX = { bg: 'transparent', sc: '#8f8f8f', tc: '#d4d4d4', s: ' ' }

interface URow { kind: 'hunk', text: string }
interface LRow { kind: 'line', a: string, b: string, t: string, bg: string, sc: string, tc: string, s: string }

const unified = computed(() => {
  const rows: (URow | LRow)[] = []
  for (const h of props.hunks) {
    rows.push({ kind: 'hunk', text: `@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@${h.header ? ' ' + h.header : ''}` })
    let a = h.oldStart
    let b = h.newStart
    for (const l of h.lines) {
      const t = l.slice(1)
      if (l.startsWith('+')) rows.push({ kind: 'line', a: '', b: String(b++), t, ...ADD })
      else if (l.startsWith('-')) rows.push({ kind: 'line', a: String(a++), b: '', t, ...DEL })
      else rows.push({ kind: 'line', a: String(a++), b: String(b++), t, ...CTX })
    }
  }
  return rows
})

interface Side { n: string, t: string, bg: string, c: string }
const EMPTY: Side = { n: '', t: '', bg: '#131313', c: '#d4d4d4' }

const split = computed(() => {
  const rows: { l: Side, r: Side }[] = []
  for (const h of props.hunks) {
    let a = h.oldStart
    let b = h.newStart
    let dels: Side[] = []
    let adds: Side[] = []
    const flush = () => {
      for (let i = 0; i < Math.max(dels.length, adds.length); i++) rows.push({ l: dels[i] ?? EMPTY, r: adds[i] ?? EMPTY })
      dels = []
      adds = []
    }
    for (const line of h.lines) {
      const t = line.slice(1)
      if (line.startsWith('-')) dels.push({ n: String(a++), t, bg: '#ff20561f', c: '#fff' })
      else if (line.startsWith('+')) adds.push({ n: String(b++), t, bg: '#00bc7d1f', c: '#fff' })
      else {
        flush()
        rows.push({ l: { n: String(a++), t, bg: 'transparent', c: '#d4d4d4' }, r: { n: String(b++), t, bg: 'transparent', c: '#d4d4d4' } })
      }
    }
    flush()
  }
  return rows
})
</script>

<template>
  <div class="border border-default bg-surface">
    <div class="flex flex-wrap items-center gap-[9px] border-b border-default py-[7px] pr-2 pl-3 text-[12px]">
      <HIcon
        v-if="icon"
        name="edit"
        class="text-muted"
      />
      <span class="text-muted">{{ verb }}</span><span class="min-w-0 truncate text-white">{{ path }}</span>
      <span class="flex-1" />
      <span class="text-ok">+{{ stats.add }}</span><span class="text-bad">−{{ stats.del }}</span>
      <div
        class="flex border border-default text-[11px]"
        role="group"
        aria-label="Diff view"
      >
        <button
          type="button"
          class="px-[7px] py-px"
          :class="view === 'unified' ? 'bg-raised text-white' : 'text-muted'"
          @click="view = 'unified'"
        >
          Unified
        </button>
        <button
          type="button"
          class="px-[7px] py-px"
          :class="view === 'split' ? 'bg-raised text-white' : 'text-muted'"
          @click="view = 'split'"
        >
          Split
        </button>
      </div>
    </div>
    <div
      v-if="view === 'unified'"
      class="overflow-x-auto text-[12px] leading-[1.7]"
    >
      <template
        v-for="(r, i) in unified"
        :key="i"
      >
        <div
          v-if="r.kind === 'hunk'"
          class="bg-[#071a20] px-3 py-0.5 text-cyan"
        >
          {{ r.text }}
        </div>
        <div
          v-else
          class="flex whitespace-pre"
          :style="{ background: r.bg }"
        >
          <span class="w-[38px] shrink-0 pr-1.5 text-right text-dimmed">{{ r.a }}</span>
          <span class="w-[38px] shrink-0 pr-2.5 text-right text-dimmed">{{ r.b }}</span>
          <span
            class="w-3.5 shrink-0"
            :style="{ color: r.sc }"
          >{{ r.s }}</span>
          <span :style="{ color: r.tc }">{{ r.t }}</span>
        </div>
      </template>
    </div>
    <div
      v-else
      class="overflow-x-auto text-[12px] leading-[1.7]"
    >
      <div
        v-for="(r, i) in split"
        :key="i"
        class="grid grid-cols-2 whitespace-pre"
      >
        <div
          class="flex overflow-hidden border-r border-default"
          :style="{ background: r.l.bg }"
        >
          <span class="w-9 shrink-0 pr-2.5 text-right text-dimmed">{{ r.l.n }}</span><span :style="{ color: r.l.c }">{{ r.l.t }}</span>
        </div>
        <div
          class="flex overflow-hidden"
          :style="{ background: r.r.bg }"
        >
          <span class="w-9 shrink-0 pr-2.5 text-right text-dimmed">{{ r.r.n }}</span><span :style="{ color: r.r.c }">{{ r.r.t }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
