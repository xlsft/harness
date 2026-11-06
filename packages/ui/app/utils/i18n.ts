// The web UI's strings. English text is the key (`tt('Save')`), so English needs no catalog and a missing translation falls back to
// what the code says. Placeholders are positional: `tt('Connect to {0}', [name])`, so a translation can move them. A locale's
// strings live in app/i18n/locales/<locale>.json, a flat map from the English text; scripts/i18n-extract.mjs keeps
// app/i18n/catalog.json, the list of every string, and a test keeps it honest. There is no pack but English yet.
//
// A language is chosen for the whole app and applies on the next load: strings are read once where they are used (menus built in
// setup, labels at module level), so changing it reloads the window, which is also why none of this needs to be reactive.

const LOCALE_KEY = 'harness-locale'

export type Pack = Record<string, string>

// The packs that ship. Adding one: a JSON file in app/i18n/locales, and a line here.
const PACKS: Record<string, { name: string, load: () => Pack }> = {}

let active: Pack | null = null
let locale = 'en'
let loaded = false

function readStoredLocale(): string {
  try {
    return globalThis.localStorage?.getItem(LOCALE_KEY) ?? ''
  } catch {
    return ''
  }
}

function ensureLoaded() {
  if (loaded) return
  loaded = true
  const stored = readStoredLocale()
  const pack = stored && Object.hasOwn(PACKS, stored) ? PACKS[stored] : undefined
  if (pack) {
    locale = stored
    active = pack.load()
  }
}

function fill(text: string, values?: readonly unknown[]): string {
  if (!values?.length) return text
  return text.replace(/\{(\d+)\}/g, (whole, i) => {
    const v = values[Number(i)]
    return v === undefined || v === null ? whole : String(v)
  })
}

// Translates English text, filling {0}, {1}... from values.
export function tt(text: string, values?: readonly unknown[]): string {
  ensureLoaded()
  return fill(active?.[text] ?? text, values)
}

// The locale in use ("en" without a pack), for <html lang>.
export function currentLocale(): string {
  ensureLoaded()
  return locale
}

// What can be chosen in Settings. English is always there.
export function availableLocales(): { value: string, label: string }[] {
  return [{ value: 'en', label: 'English' }, ...Object.entries(PACKS).map(([value, p]) => ({ value, label: p.name }))]
}

// Remembers a language for the next load; the caller reloads the window.
export function chooseLocale(value: string) {
  try {
    if (value === 'en') globalThis.localStorage?.removeItem(LOCALE_KEY)
    else globalThis.localStorage?.setItem(LOCALE_KEY, value)
  } catch { /* private mode: it stays as it is */ }
}

// For tests: use this pack from now on (null: back to English).
export function useTestPack(pack: Pack | null, name = 'test') {
  loaded = true
  active = pack
  locale = pack ? name : 'en'
}
