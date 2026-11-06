import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it } from 'vitest'
import { availableLocales, currentLocale, tt, useTestPack } from '../app/utils/i18n'

afterEach(() => useTestPack(null))

describe('tt', () => {
  it('gives English back, filling its placeholders', () => {
    expect(tt('Save')).toBe('Save')
    expect(tt('Connect to {0} as {1}', ['box', 'me'])).toBe('Connect to box as me')
    expect(tt('{0} of {1}', [0, 5])).toBe('0 of 5')
  })

  it('leaves a placeholder it has no value for', () => {
    expect(tt('Connect to {0}')).toBe('Connect to {0}')
    expect(tt('{0} and {1}', ['a'])).toBe('a and {1}')
  })

  it('reads a pack, where placeholders may move and a missing string stays English', () => {
    useTestPack({ 'Connect to {0} as {1}': '{1} подключается к {0}', 'Save': 'Сохранить' }, 'ru')
    expect(tt('Save')).toBe('Сохранить')
    expect(tt('Connect to {0} as {1}', ['box', 'me'])).toBe('me подключается к box')
    expect(tt('Cancel')).toBe('Cancel')
    expect(currentLocale()).toBe('ru')
  })

  it('does not take an object property for a translation', () => {
    useTestPack({}, 'x')
    expect(tt('constructor')).toBe('constructor')
  })

  it('always offers English', () => {
    expect(availableLocales()[0]).toEqual({ value: 'en', label: 'English' })
  })
})

describe('the catalog', () => {
  it('is what the code says, and no template holds plain text', () => {
    const script = fileURLToPath(new URL('../scripts/i18n-extract.mjs', import.meta.url))
    let out = ''
    try {
      execFileSync(process.execPath, [script, '--check'], { encoding: 'utf8', stdio: 'pipe' })
    } catch (e) {
      out = String((e as { stderr?: string }).stderr)
    }
    expect(out).toBe('')
  }, 60_000)

  it('has the strings of this app', () => {
    const catalog = JSON.parse(readFileSync(new URL('../app/i18n/catalog.json', import.meta.url), 'utf8')) as string[]
    expect(catalog.length).toBeGreaterThan(500)
    expect(new Set(catalog).size).toBe(catalog.length)
  })
})
