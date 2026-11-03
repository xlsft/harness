<script setup lang="ts">
// Settings > General > Appearance: the mode (follow the system, always light, always dark) and the theme used
// for each kind, picked from previews. Kept in this browser (composables/useAppearance.ts).
import { THEMES, type AppearanceMode, type ThemeKind } from '~/themes'

const { appearance, kind, setMode, setTheme } = useAppearance()

const MODES = [
  { value: 'system', label: 'System', hint: 'follows the OS' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' }
]
const mode = computed({
  get: () => appearance.value.mode,
  set: (v: string) => setMode(v as AppearanceMode)
})

const KINDS: { kind: ThemeKind, title: string, desc: string }[] = [
  { kind: 'light', title: 'Light theme', desc: 'Used in light mode, and in System mode while the OS is light.' },
  { kind: 'dark', title: 'Dark theme', desc: 'Used in dark mode, and in System mode while the OS is dark.' }
]
</script>

<template>
  <h2 class="set-h">
    Appearance
  </h2>
  <p class="set-sub">
    How the app looks in this browser.
  </p>
  <div class="set-row">
    <div class="set-label">
      <span>Mode</span>
      <span class="set-desc">Light, dark, or the same as the system.</span>
    </div>
    <div class="w-[200px]">
      <HSelect
        v-model="mode"
        :options="MODES"
        aria-label="Appearance mode"
      />
    </div>
  </div>
  <div
    v-for="k in KINDS"
    :key="k.kind"
    class="set-row flex-col !items-stretch gap-2.5"
  >
    <div class="set-label">
      <span>{{ k.title }}<span
        v-if="kind === k.kind"
        class="ml-2 text-[11.5px] text-muted"
      >in use</span></span>
      <span class="set-desc">{{ k.desc }}</span>
    </div>
    <div
      role="radiogroup"
      :aria-label="k.title"
      class="grid grid-cols-[repeat(auto-fill,minmax(170px,1fr))] gap-2.5"
    >
      <button
        v-for="t in THEMES.filter(x => x.kind === k.kind)"
        :key="t.id"
        type="button"
        role="radio"
        :aria-checked="appearance[k.kind] === t.id"
        class="flex flex-col border text-left outline-none focus-visible:border-accent"
        :class="appearance[k.kind] === t.id ? 'border-accent' : 'border-line hover:border-line-strong'"
        @click="setTheme(k.kind, t.id)"
      >
        <ThemePreview :theme="t" />
        <span class="flex h-8 items-center gap-2 border-t border-line px-2.5 text-[12.5px]">
          <span class="min-w-0 flex-1 truncate text-strong">{{ t.name }}</span>
          <HIcon
            v-if="appearance[k.kind] === t.id"
            name="check"
            :size="13"
            class="text-accent"
          />
        </span>
      </button>
    </div>
  </div>
</template>
