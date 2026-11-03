import { describe, expect, it } from 'vitest'
import { THEME_TOKENS } from '../app/themes'
import { parseJsonc, themeFromVsCode } from '../app/themes/vscode'

const SAMPLE = `{
  // A theme as VS Code writes it: comments, trailing commas, translucent colours.
  "name": "Night Owl Lite",
  "type": "dark",
  "colors": {
    "editor.background": "#011627",
    "editor.foreground": "#d6deeb",
    "sideBar.background": "#011627",
    "list.hoverBackground": "#ffffff20", /* white at 1/8 over the background */
    "button.background": "#7e57c2",
    "button.foreground": "#ffffff",
    "terminal.ansiGreen": "#22da6e",
    "terminal.ansiRed": "#ef5350",
  },
  "tokenColors": [],
}`

describe('VS Code themes', () => {
  it('parses JSONC without touching strings', () => {
    expect(parseJsonc('{"a": "http://x//y", /* c */ "b": [1,], }')).toEqual({ a: 'http://x//y', b: [1] })
  })

  it('maps workbench colours onto every token', () => {
    const t = themeFromVsCode(SAMPLE)
    expect(t).toMatchObject({ id: 'vscode-night-owl-lite', name: 'Night Owl Lite', kind: 'dark' })
    for (const k of THEME_TOKENS) expect(t.tokens[k]).toMatch(/^#[0-9a-f]{6}$/)
    expect(t.tokens.ground).toBe('#011627')
    expect(t.tokens.text).toBe('#d6deeb')
    expect(t.tokens.accent).toBe('#7e57c2')
    expect(t.tokens.ok).toBe('#22da6e')
    // #ffffff20 over #011627: about 12.5% white.
    expect(t.tokens.hover).toBe('#213342')
    expect(t.radius).toBeUndefined()
  })

  it('guesses the kind from the background and rejects non-themes', () => {
    expect(themeFromVsCode('{"colors": {"editor.background": "#fdf6e3"}}', 'Solarized Light.json')).toMatchObject({ kind: 'light', name: 'Solarized Light' })
    expect(() => themeFromVsCode('{"name": "x"}')).toThrow(/no "colors"/)
  })
})
