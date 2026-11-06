<script setup lang="ts">
// Settings > General > Appearance: the mode (follow the system, always light, always dark) and the theme used
// for each kind, picked from previews; VS Code colour themes can be imported. Kept in this browser
// (composables/useAppearance.ts).
import { CODE_SIZE_DEFAULT, DEFAULT_FONTS, UI_SIZE_DEFAULT, themeById, type AppearanceMode, type ThemeKind } from '~/themes'
import { themeFromVsCode } from '~/themes/vscode'

const { appearance, kind, themes, isCustom, importTheme, removeTheme, setMode, setTheme, setSizes } = useAppearance()

// Text sizes: the interface scales the whole app; code has its own size and font.
const UI_SIZES = [12, 13, 14, 15, 16].map(n => ({ value: String(n), label: `${n} px`, hint: n === UI_SIZE_DEFAULT ? 'default' : undefined }))
const CODE_SIZES = [{ value: '0', label: tt('Default · {0} px', [CODE_SIZE_DEFAULT]) }, ...[11, 12, 13, 14, 15, 16].map(n => ({ value: String(n), label: `${n} px` }))]
const uiSize = computed({ get: () => String(appearance.value.uiSize), set: (v: string) => setSizes({ uiSize: Number(v) }) })
const codeSize = computed({ get: () => String(appearance.value.codeSize), set: (v: string) => setSizes({ codeSize: Number(v) }) })
const codeFont = ref(appearance.value.codeFont)
watch(codeFont, v => setSizes({ codeFont: v }))
const terminalFont = ref(appearance.value.terminalFont)
watch(terminalFont, v => setSizes({ terminalFont: v }))
// The terminal font the current theme brings, for the placeholder.
const themeTerminalFont = computed(() => (themeById(appearance.value[kind.value], kind.value, themes.value).fonts?.terminal ?? DEFAULT_FONTS.terminal).split(',')[0]!.replace(/['"]/g, '').trim())
const previewThemes = computed(() => (['light', 'dark'] as const).map(k => themeById(appearance.value[k], k, themes.value)))
const previewFont = (t: ReturnType<typeof themeById>) => {
  const base = t.fonts?.mono ?? DEFAULT_FONTS.mono
  const f = appearance.value.codeFont.trim()
  return f ? `${/[,'"]/.test(f) ? f : `'${f}'`}, ${base}` : base
}
const toast = useToast()

const screenReader = useScreenReader()
const locales = availableLocales()
function pickLocale(v: unknown) {
  chooseLocale(String(v))
  window.location.reload()
}

const picker = ref<HTMLInputElement | null>(null)
async function onPick(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  try {
    const t = themeFromVsCode(await file.text(), file.name)
    importTheme(t)
    toast.add({ title: tt('Imported {0}', [t.name]), description: tt('Now your {0} theme.', [t.kind]), color: 'neutral' })
  } catch (err) {
    toast.add({ title: tt('Could not import the theme'), description: err instanceof Error ? err.message : String(err), color: 'error' })
  }
}

const MODES = [
  { value: 'system', label: tt('System'), hint: 'follows the OS' },
  { value: 'light', label: tt('Light') },
  { value: 'dark', label: tt('Dark') }
]
const mode = computed({
  get: () => appearance.value.mode,
  set: (v: string) => setMode(v as AppearanceMode)
})

const KINDS: { kind: ThemeKind, title: string, desc: string }[] = [
  { kind: 'light', title: tt('Light theme'), desc: tt('Used in light mode, and in System mode while the OS is light.') },
  { kind: 'dark', title: tt('Dark theme'), desc: tt('Used in dark mode, and in System mode while the OS is dark.') }
]
</script>

<template>
  <div class="flex items-start gap-3">
    <h2 class="set-h flex-1">
      {{ tt('Appearance') }}
    </h2>
    <HBtn
      size="md"
      @click="picker?.click()"
    >
      <HIcon
        name="upload"
        :size="12"
      />{{ tt('Import VS Code theme…') }}
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
    {{ tt('How the app looks in this browser.') }}
  </p>
  <div class="set-row">
    <div class="set-label">
      <span>{{ tt('Mode') }}</span>
      <span class="set-desc">{{ tt('Light, dark, or the same as the system.') }}</span>
    </div>
    <div class="w-[200px]">
      <HSelect
        v-model="mode"
        :options="MODES"
        :aria-label="tt('Appearance mode')"
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
      >{{ tt('in use') }}</span></span>
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
            :aria-label="tt('Remove {0}', [t.name])"
            :title="tt('Remove')"
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
  <div class="set-row">
    <div class="set-label">
      <span>{{ tt('Text size') }}</span>
      <span class="set-desc">{{ tt('The interface\'s text; everything else scales with it.') }}</span>
    </div>
    <div class="w-[200px]">
      <HSelect
        v-model="uiSize"
        :options="UI_SIZES"
        :aria-label="tt('Text size')"
      />
    </div>
  </div>

  <h2 class="set-h mt-8">
    {{ tt('Accessibility') }}
  </h2>
  <p class="set-sub">
    {{ tt('The color-blind themes are in the theme lists above.') }}
  </p>
  <div class="set-row">
    <div class="set-label">
      <span>{{ tt('Screen reader mode') }}</span>
      <span class="set-desc">{{ tt('Says when the agent starts, finishes or needs you, and reads its answers; takes away motion and decoration. For this browser.') }}</span>
    </div>
    <HToggle
      :model-value="screenReader.enabled.value"
      :label="tt('Screen reader mode')"
      @update:model-value="screenReader.set($event)"
    />
  </div>
  <div
    v-if="locales.length > 1"
    class="set-row"
  >
    <div class="set-label">
      <span>{{ tt('Language') }}</span>
    </div>
    <HSelect
      :model-value="currentLocale()"
      :options="locales"
      :aria-label="tt('Language')"
      @update:model-value="pickLocale"
    />
  </div>

  <h2 class="set-h mt-8">
    {{ tt('Code') }}
  </h2>
  <p class="set-sub">
    {{ tt('Code blocks, diffs, inline code and command output, in your light and dark theme.') }}
  </p>
  <div class="set-row !items-stretch gap-3 max-md:flex-col">
    <div
      v-for="t in previewThemes"
      :key="t.id"
      class="flex min-w-0 flex-1 flex-col gap-1.5"
    >
      <span class="text-[11.5px] text-muted">{{ t.name }}</span>
      <CodePreview
        :theme="t"
        :size="appearance.codeSize || CODE_SIZE_DEFAULT"
        :font="previewFont(t)"
      />
    </div>
  </div>
  <div class="set-row">
    <div class="set-label">
      <span>{{ tt('Code font') }}</span>
      <span class="set-desc">{{ tt('A monospace font installed on this computer; empty: the theme\'s.') }}</span>
    </div>
    <input
      v-model.lazy="codeFont"
      placeholder="e.g. JetBrains Mono"
      :aria-label="tt('Code font')"
      autocapitalize="off"
      autocorrect="off"
      spellcheck="false"
      maxlength="120"
      class="h-8 w-[200px] border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none placeholder:text-muted focus:border-accent"
    >
  </div>
  <div class="set-row">
    <div class="set-label">
      <span>{{ tt('Terminal font') }}</span>
      <span class="set-desc">{{ tt('Any installed font; empty: the theme\'s Nerd Font, with icons for prompts and tools.') }}</span>
    </div>
    <input
      v-model.lazy="terminalFont"
      :placeholder="themeTerminalFont"
      :aria-label="tt('Terminal font')"
      autocapitalize="off"
      autocorrect="off"
      spellcheck="false"
      maxlength="120"
      class="h-8 w-[200px] border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none placeholder:text-muted focus:border-accent"
    >
  </div>
  <div class="set-row">
    <div class="set-label">
      <span>{{ tt('Code font size') }}</span>
      <span class="set-desc">{{ tt('Code and the terminal; independent of the text size.') }}</span>
    </div>
    <div class="w-[200px]">
      <HSelect
        v-model="codeSize"
        :options="CODE_SIZES"
        :aria-label="tt('Code font size')"
      />
    </div>
  </div>
</template>
