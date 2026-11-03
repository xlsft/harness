<script setup lang="ts">
// Settings > General > Appearance: the mode (follow the system, always light, always dark) and the theme used
// for each kind, picked from previews; VS Code colour themes can be imported. Kept in this browser
// (composables/useAppearance.ts).
import type { AppearanceMode, ThemeKind } from '~/themes'
import { themeFromVsCode } from '~/themes/vscode'

const { appearance, kind, themes, isCustom, importTheme, removeTheme, setMode, setTheme } = useAppearance()
const toast = useToast()

const picker = ref<HTMLInputElement | null>(null)
async function onPick(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  try {
    const t = themeFromVsCode(await file.text(), file.name)
    importTheme(t)
    toast.add({ title: `Imported ${t.name}`, description: `Now your ${t.kind} theme.`, color: 'neutral' })
  } catch (err) {
    toast.add({ title: 'Could not import the theme', description: err instanceof Error ? err.message : String(err), color: 'error' })
  }
}

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
  <div class="flex items-start gap-3">
    <h2 class="set-h flex-1">
      Appearance
    </h2>
    <HBtn
      size="md"
      @click="picker?.click()"
    >
      <HIcon
        name="upload"
        :size="12"
      />Import VS Code theme…
    </HBtn>
    <input
      ref="picker"
      type="file"
      accept=".json,application/json"
      class="hidden"
      @change="onPick"
    >
  </div>
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
        v-for="t in themes.filter(x => x.kind === k.kind)"
        :key="t.id"
        type="button"
        role="radio"
        :aria-checked="appearance[k.kind] === t.id"
        class="flex flex-col overflow-hidden border text-left outline-none focus-visible:border-accent"
        :class="appearance[k.kind] === t.id ? 'border-accent' : 'border-line hover:border-line-strong'"
        @click="setTheme(k.kind, t.id)"
      >
        <ThemePreview :theme="t" />
        <span class="flex h-8 items-center gap-2 border-t border-line px-2.5 text-[12.5px]">
          <span class="min-w-0 flex-1 truncate text-strong">{{ t.name }}</span>
          <span
            v-if="isCustom(t)"
            role="button"
            :aria-label="`Remove ${t.name}`"
            title="Remove"
            class="text-muted hover:text-bad"
            @click.stop="removeTheme(t)"
          ><HIcon
            name="trash"
            :size="12"
          /></span>
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
