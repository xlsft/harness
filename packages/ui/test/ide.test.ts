import { describe, expect, it } from 'vitest'
import { ideMention, parseFileRef } from '../app/lib/ide'

describe('ideMention', () => {
  it('is relative inside the session folder', () => {
    expect(ideMention('/repo/src/a.ts', 0, 0, '/repo')).toBe('src/a.ts')
    expect(ideMention('/repo/src/a.ts', 10, 20, '/repo/')).toBe('src/a.ts#L10-20')
    expect(ideMention('/repo/src/a.ts', 7, 7, '/repo')).toBe('src/a.ts#L7')
  })
  it('stays absolute outside it', () => {
    expect(ideMention('/other/a.ts', 1, 2, '/repo')).toBe('/other/a.ts#L1-2')
    expect(ideMention('/repo-two/a.ts', 0, 0, '/repo')).toBe('/repo-two/a.ts')
  })
  it('escapes spaces', () => {
    expect(ideMention('/repo/my file.ts', 3, 0, '/repo')).toBe('my\\ file.ts#L3')
  })
})

describe('parseFileRef', () => {
  it('reads paths and lines', () => {
    expect(parseFileRef('src/a.ts', '/repo')).toEqual({ path: '/repo/src/a.ts', line: 0, endLine: 0 })
    expect(parseFileRef('src/a.ts:12', '/repo')).toEqual({ path: '/repo/src/a.ts', line: 12, endLine: 0 })
    expect(parseFileRef('src/a.ts:12-20', '/repo')).toEqual({ path: '/repo/src/a.ts', line: 12, endLine: 20 })
    expect(parseFileRef('src/a.ts#L12-L20', '/repo')).toEqual({ path: '/repo/src/a.ts', line: 12, endLine: 20 })
    expect(parseFileRef('src/a.ts:12:4', '/repo')).toEqual({ path: '/repo/src/a.ts', line: 12, endLine: 0 })
    expect(parseFileRef('/abs/a.go:3', '/repo')).toEqual({ path: '/abs/a.go', line: 3, endLine: 0 })
    expect(parseFileRef('./a.go', '/repo')).toEqual({ path: '/repo/a.go', line: 0, endLine: 0 })
    expect(parseFileRef('main.go', '/repo')).toEqual({ path: '/repo/main.go', line: 0, endLine: 0 })
  })
  it('leaves other code alone', () => {
    for (const text of ['useState', 'npm test', 'a = b', 'foo()', '1.2.3', '--flag', 'https://example.com/a.ts', '']) {
      expect(parseFileRef(text, '/repo')).toBeNull()
    }
  })
})
