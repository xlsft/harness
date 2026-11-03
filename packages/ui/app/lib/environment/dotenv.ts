// .env parsing with the client's rules (packages/client/environment/dotenv.go), for the mock and for instant
// feedback in the editor. The client stays the authority: UpdateEnvironment validates again.

import type { EnvLineError } from './types'

export interface EnvVar {
  name: string
  value: string
  // 1-based line the definition starts on.
  line: number
}

const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/
const MAX_BYTES = 256 * 1024
const MAX_VARS = 1000

function unquoted(s: string) {
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '#' && (i === 0 || s[i - 1] === ' ' || s[i - 1] === '\t')) {
      s = s.slice(0, i)
      break
    }
  }
  return s.trim()
}

const ESCAPES: Record<string, string> = { 'n': '\n', 'r': '\r', 't': '\t', '"': '"', '\\': '\\', '$': '$' }

// NAME=value lines, optional "export ", "#" comments, unquoted / "double" (escapes, multi-line) / 'single'
// (literal, multi-line) values; a later definition wins. Returns the variables, or every line error.
export function parseEnv(text: string): { vars: EnvVar[], errors: EnvLineError[] } {
  const errors: EnvLineError[] = []
  const fail = (line: number, message: string) => errors.push({ line, message })
  if (new TextEncoder().encode(text).length > MAX_BYTES) return { vars: [], errors: [{ line: 1, message: `the text is larger than ${MAX_BYTES >> 10} KiB` }] }
  const lines = text.replace(/^\uFEFF/, '').replace(/\r\n/g, '\n').split('\n')
  const vars: EnvVar[] = []
  const index = new Map<string, number>()
  for (let i = 0; i < lines.length; i++) {
    const lineNo = i + 1
    let line = lines[i]!.trim()
    if (!line || line.startsWith('#')) continue
    const exp = /^export[ \t]+(.*)$/.exec(line)
    if (exp) line = exp[1]!.trim()
    const eq = line.indexOf('=')
    const name = (eq < 0 ? line : line.slice(0, eq)).trim()
    if (eq < 0) {
      fail(lineNo, NAME.test(name) ? `${name} has no value (expected ${name}=value)` : 'expected NAME=value')
      continue
    }
    if (!name) {
      fail(lineNo, 'missing variable name before =')
      continue
    }
    if (!NAME.test(name)) {
      fail(lineNo, `invalid variable name "${name}" (letters, digits and _, not starting with a digit)`)
      continue
    }
    if (name.toUpperCase().startsWith('__HARNESS_')) {
      fail(lineNo, `${name} is reserved by harness`)
      continue
    }
    const rest = line.slice(eq + 1).replace(/^[ \t]+/, '')
    let value: string
    if (rest.startsWith('"') || rest.startsWith('\'')) {
      const q = rest[0]!
      let s = rest.slice(1)
      let out = ''
      let at = i
      let closed = false
      let after = ''
      for (;;) {
        for (let j = 0; j < s.length; j++) {
          const c = s[j]!
          if (c === q) {
            closed = true
            after = s.slice(j + 1)
            break
          }
          if (q === '"' && c === '\\' && j + 1 < s.length) {
            j++
            out += ESCAPES[s[j]!] ?? `\\${s[j]}`
          } else {
            out += c
          }
        }
        if (closed) break
        at++
        if (at >= lines.length) break
        out += '\n'
        s = lines[at]!
      }
      if (!closed) {
        fail(lineNo, `unterminated ${q === '"' ? 'double' : 'single'}-quoted value (no closing ${q})`)
        i = lines.length
        continue
      }
      const tail = after.trim()
      if (tail && !tail.startsWith('#')) fail(at + 1, `unexpected text after the closing quote of ${name}`)
      value = out
      i = at
    } else {
      value = unquoted(rest)
    }
    if (value.includes('\0')) {
      fail(lineNo, `the value of ${name} contains a NUL character`)
      continue
    }
    const j = index.get(name)
    if (j !== undefined) {
      vars[j] = { name, value, line: lineNo }
      continue
    }
    if (vars.length >= MAX_VARS) {
      fail(lineNo, `more than ${MAX_VARS} variables`)
      break
    }
    index.set(name, vars.length)
    vars.push({ name, value, line: lineNo })
  }
  return errors.length ? { vars: [], errors } : { vars, errors }
}
