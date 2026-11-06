import { describe, expect, it } from 'vitest'
import { announcements, momentOf, type SessionMoment } from '../app/lib/a11y/announce'
import type { SessionState } from '../app/lib/session/types'
import { THEMES, type Theme } from '../app/themes'

const moment = (over: Partial<SessionMoment> = {}): SessionMoment => ({ id: 's', busy: false, waiting: [], lastAnswerId: '', lastAnswer: '', lastError: '', ...over })

describe('what a screen reader hears', () => {
  it('says nothing about a session just opened', () => {
    expect(announcements(null, moment({ lastAnswerId: 'a', lastAnswer: 'old' }))).toEqual([])
    expect(announcements(moment({ id: 'other' }), moment({ lastAnswerId: 'a', lastAnswer: 'old' }))).toEqual([])
  })

  it('says a turn started and ended', () => {
    expect(announcements(moment(), moment({ busy: true }))).toEqual([{ text: 'The agent is working.', priority: 'polite' }])
    expect(announcements(moment({ busy: true }), moment())).toEqual([{ text: 'The agent is done.', priority: 'polite' }])
  })

  it('speaks over anything else when the work waits for the user, once', () => {
    const waiting = moment({ busy: true, waiting: ['permission:p1'] })
    expect(announcements(moment({ busy: true }), waiting)).toEqual([{ text: 'The agent needs permission to go on.', priority: 'assertive' }])
    expect(announcements(waiting, waiting)).toEqual([])
    expect(announcements(waiting, moment({ busy: true, waiting: ['permission:p1', 'browserSite:b1'] }))[0]?.text).toMatch(/site/)
  })

  it('does not say "done" while something waits', () => {
    expect(announcements(moment({ busy: true }), moment({ waiting: ['question:q'] })).map(a => a.text)).not.toContain('The agent is done.')
  })

  it('reads an answer once, plain and cut to a length', () => {
    const said = announcements(moment(), moment({ lastAnswerId: 'a1', lastAnswer: 'See `main.go`:\n\n```go\nfunc main() {}\n```\n**Done**' }))
    expect(said[0]?.text).toBe('See main.go: code block Done')
    expect(announcements(moment({ lastAnswerId: 'a1' }), moment({ lastAnswerId: 'a1', lastAnswer: 'x' }))).toEqual([])
    const long = announcements(moment(), moment({ lastAnswerId: 'a2', lastAnswer: 'word '.repeat(500) }))
    expect(long[0]!.text.length).toBeLessThan(700)
    expect(long[0]!.text).toMatch(/The rest is in the conversation\.$/)
  })

  it('says a new error at once', () => {
    expect(announcements(moment(), moment({ lastError: 'boom' }))).toEqual([{ text: 'Error: boom', priority: 'assertive' }])
    expect(announcements(moment({ lastError: 'boom' }), moment({ lastError: 'boom' }))).toEqual([])
  })

  it('reads a session as moments', () => {
    const s = {
      meta: { id: 's', status: 'working' },
      items: [
        { kind: 'assistant', id: 'a0', blocks: [{ type: 'text', text: 'old' }], streaming: false },
        { kind: 'permission', id: 'p', state: 'pending' },
        { kind: 'browserSite', id: 'b', state: 'resolved' },
        { kind: 'assistant', id: 'a1', blocks: [{ type: 'text', text: 'streaming' }], streaming: true }
      ]
    } as unknown as SessionState
    expect(momentOf(s)).toMatchObject({ id: 's', busy: true, waiting: ['permission:p'], lastAnswerId: 'a0', lastAnswer: 'old' })
  })
})

// sRGB to linear and back, Machado et al. 2009 at full severity, and distance in OKLab: how far apart two colours look to someone
// with that colour vision. The status colours must stay told apart where the default palette may not.
const CVD: Record<string, number[][]> = {
  protanopia: [[0.152286, 1.052583, -0.204868], [0.114503, 0.786281, 0.099216], [-0.003882, -0.048116, 1.051998]],
  deuteranopia: [[0.367322, 0.860646, -0.227968], [0.280085, 0.672501, 0.047413], [-0.011820, 0.042940, 0.968881]],
  tritanopia: [[1.255528, -0.076749, -0.178779], [-0.078411, 0.930809, 0.147602], [0.004733, 0.691367, 0.303900]]
}
const linear = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
function rgb(hex: string): number[] {
  const n = Number.parseInt(hex.slice(1, 7), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255].map(v => linear(v / 255))
}
function oklab([r, g, b]: number[]): number[] {
  const l = Math.cbrt(0.4122214708 * r! + 0.5363325363 * g! + 0.0514459929 * b!)
  const m = Math.cbrt(0.2119034982 * r! + 0.6806995451 * g! + 0.1073969566 * b!)
  const s = Math.cbrt(0.0883024619 * r! + 0.2817188376 * g! + 0.6299787005 * b!)
  return [0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s, 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s, 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s]
}
function seen(hex: string, vision: string): number[] {
  const c = rgb(hex)
  const M = CVD[vision]!
  return oklab(M.map(row => Math.min(1, Math.max(0, row[0]! * c[0]! + row[1]! * c[1]! + row[2]! * c[2]!))))
}
function apart(a: string, b: string, vision: string): number {
  const [x, y] = [seen(a, vision), seen(b, vision)]
  return Math.hypot(x[0]! - y[0]!, x[1]! - y[1]!, x[2]! - y[2]!)
}
function contrast(a: string, b: string): number {
  const lum = (h: string) => {
    const [r, g, bl] = rgb(h)
    return 0.2126 * r! + 0.7152 * g! + 0.0722 * bl!
  }
  const [l1, l2] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (l1! + 0.05) / (l2! + 0.05)
}

describe('colour-blind themes', () => {
  const themes = THEMES.filter(t => t.id.includes('colorblind'))

  it('exist for both kinds and both groups of colour vision', () => {
    expect(themes.map(t => t.id).sort()).toEqual(['colorblind-blue-yellow-dark', 'colorblind-blue-yellow-light', 'colorblind-red-green-dark', 'colorblind-red-green-light'])
  })

  const visions = (t: Theme) => (t.id.includes('red-green') ? ['protanopia', 'deuteranopia'] : ['tritanopia'])

  it.each(['colorblind-red-green-dark', 'colorblind-red-green-light', 'colorblind-blue-yellow-dark', 'colorblind-blue-yellow-light'])('%s keeps status colours apart', (id) => {
    const t = themes.find(x => x.id === id)!
    for (const v of visions(t)) {
      for (const [a, b] of [['ok', 'bad'], ['ok', 'warn'], ['bad', 'warn'], ['accent', 'bad']] as const) {
        expect(apart(t.tokens[a], t.tokens[b], v), `${a}/${b} under ${v}`).toBeGreaterThan(0.1)
      }
    }
  })

  it.each(['colorblind-red-green-dark', 'colorblind-red-green-light', 'colorblind-blue-yellow-dark', 'colorblind-blue-yellow-light'])('%s can be read', (id) => {
    const t = themes.find(x => x.id === id)!
    for (const token of ['ok', 'bad', 'warn', 'accent', 'text', 'strong'] as const) {
      expect(contrast(t.tokens[token], t.tokens.surface), token).toBeGreaterThanOrEqual(3)
    }
    expect(contrast(t.tokens.text, t.tokens.surface)).toBeGreaterThanOrEqual(4.5)
  })

  it('is better than the default where it matters', () => {
    const dflt = THEMES.find(t => t.id === 'xlsft-dark')!
    const cb = themes.find(t => t.id === 'colorblind-red-green-dark')!
    expect(apart(cb.tokens.ok, cb.tokens.bad, 'deuteranopia')).toBeGreaterThan(apart(dflt.tokens.ok, dflt.tokens.bad, 'deuteranopia'))
  })
})
