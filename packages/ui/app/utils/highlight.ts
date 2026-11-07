// Tiny syntax and output highlighters in the design's palette (Md.dc.html, Tool.dc.html):
// keywords pink, strings emerald, types cyan, numbers amber, comments grey, calls white, the rest #d4d4d4.

export interface Token {
  t: string
  c: string
}

export const PALETTE = {
  text: 'var(--color-text)',
  white: 'var(--color-strong)',
  muted: 'var(--color-muted)',
  keyword: 'var(--color-pink)',
  string: 'var(--color-ok)',
  type: 'var(--color-cyan)',
  number: 'var(--color-warn)',
  ok: 'var(--color-ok)',
  warn: 'var(--color-warn)',
  bad: 'var(--color-bad)'
}

const KEYWORDS = new Set([
  'import', 'from', 'export', 'default', 'const', 'let', 'var', 'function', 'return', 'async', 'await', 'if', 'else',
  'for', 'of', 'in', 'while', 'new', 'class', 'extends', 'type', 'interface', 'yield', 'package', 'func', 'go', 'defer',
  'struct', 'map', 'range', 'switch', 'case', 'break', 'continue', 'throw', 'try', 'catch', 'true', 'false', 'null', 'nil', 'undefined'
])

const CODE_RE = /(\/\/[^\n]*|#[^\n]*)|('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|`(?:[^`\\]|\\.)*`)|(\b\d[\d_]*(?:\.\d+)?\b)|([A-Za-z_$][\w$]*)(\s*\()?|([^\w\s'"`]+|\s+)/g

const SHELLS = new Set(['bash', 'sh', 'zsh', 'shell', 'console', 'shellscript'])
const SHELL_KEYWORDS = new Set([
  'if', 'then', 'else', 'elif', 'fi', 'for', 'in', 'do', 'done', 'while', 'until', 'case', 'esac', 'function', 'select',
  'time', 'export', 'local', 'readonly', 'declare', 'unset', 'return', 'exit', 'source', 'eval', 'exec', 'set'
])
// Comments, strings, variables and substitutions, operators, flags, numbers, words.
const SHELL_RE = /((?:^|(?<=\s))#.*)|('[^']*'?|"(?:[^"\\]|\\.)*"?|\$'(?:[^'\\]|\\.)*'?)|(\$\{[^}]*\}?|\$\(+|\$[A-Za-z_]\w*|\$[\d@#?$!*-])|(&&|\|\||\|&?|;;?|&|\d?>>?&?\d?|<<<?|<|\(|\)|`)|((?<=^|\s)--?[\w-]+(?:=\S*)?)|(\b\d+\b)|([^\s'"$&|;<>()`]+)|(\s+|\S)/g

// A shell line: the command word of each pipeline step in white, flags cyan, variables amber, keywords pink.
export function highlightShell(line: string): Token[] {
  const out: Token[] = []
  let command = true
  SHELL_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = SHELL_RE.exec(line))) {
    if (m[1]) out.push({ t: m[1], c: PALETTE.muted })
    else if (m[2]) out.push({ t: m[2], c: PALETTE.string })
    else if (m[3]) {
      out.push({ t: m[3], c: PALETTE.number })
      if (m[3].startsWith('$(')) command = true
    } else if (m[4]) {
      out.push({ t: m[4], c: PALETTE.keyword })
      command = m[4] !== '<' && !/>/.test(m[4])
    } else if (m[5]) out.push({ t: m[5], c: PALETTE.type })
    else if (m[6]) out.push({ t: m[6], c: PALETTE.number })
    else if (m[7]) {
      const w = m[7]
      if (SHELL_KEYWORDS.has(w)) {
        out.push({ t: w, c: PALETTE.keyword })
        command = !['in', 'export', 'local', 'readonly', 'declare', 'unset'].includes(w)
      } else if (command && /^[A-Za-z_]\w*=/.test(w)) {
        // An assignment before the command (FOO=1 cmd).
        out.push({ t: w, c: PALETTE.text })
      } else {
        out.push({ t: w, c: command ? PALETTE.white : PALETTE.text })
        command = false
      }
    } else out.push({ t: m[0], c: PALETTE.text })
  }
  return merge(out)
}

// Highlights one line of code. `#` comments only for shell-like languages.
export function highlightLine(line: string, lang = 'ts'): Token[] {
  if (SHELLS.has(lang)) return highlightShell(line)
  const shell = ['bash', 'sh', 'shell', 'yaml', 'yml'].includes(lang)
  const out: Token[] = []
  CODE_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = CODE_RE.exec(line))) {
    if (m[1]) {
      if (m[1].startsWith('#') && !shell) {
        out.push({ t: m[1], c: PALETTE.text })
      } else {
        out.push({ t: m[1], c: PALETTE.muted })
      }
    } else if (m[2]) {
      out.push({ t: m[2], c: PALETTE.string })
    } else if (m[3]) {
      out.push({ t: m[3], c: PALETTE.number })
    } else if (m[4]) {
      const word = m[4]
      const c = KEYWORDS.has(word)
        ? (word === 'true' || word === 'false' ? PALETTE.number : PALETTE.keyword)
        : m[5]
          ? PALETTE.white
          : /^[A-Z]/.test(word) ? PALETTE.type : PALETTE.text
      out.push({ t: word, c })
      if (m[5]) out.push({ t: m[5], c: PALETTE.text })
    } else {
      out.push({ t: m[0], c: PALETTE.text })
    }
  }
  return merge(out)
}

export function highlightCode(code: string, lang = 'ts'): Token[][] {
  return code.replace(/\n$/, '').split('\n').map(l => highlightLine(l, lang))
}

const OUTPUT_RE = /(^\s*✓|^\s*✔)|(^\s*↓|^\s*⚠)|(\d+ passed)|(\d+ skipped|\[skipped\])|(\(\d+\))|(\bFAIL\b|\bfailed\b|\bError\b)|(^\s*(?:Test Files|Tests|Duration))/g

// Terminal control sequences: CSI (colours, cursor moves), OSC (titles, links) and two-byte ESC ones. The client strips
// them from new output; this also cleans transcripts written before it did.
// eslint-disable-next-line no-control-regex
const TERMINAL_CODE_RE = /\x1B\[[0-9;:?<=>!]*[ -/]*[@-~]|\x1B\][^\x07\x1B]*(?:\x07|\x1B\\)|\x1B[()*+][0-9A-Za-z]|\x1B[=>78DEHMNOZc]/g

export function stripTerminalCodes(text: string): string {
  return text.includes('\x1B') ? text.replace(TERMINAL_CODE_RE, '') : text
}

// Command output (test runners, builds): ✓ / passed in emerald, ↓ / skipped in amber, FAIL in rose, labels grey.
export function highlightOutput(text: string): Token[][] {
  return stripTerminalCodes(text).replace(/\n$/, '').split('\n').map((line) => {
    const out: Token[] = []
    let last = 0
    OUTPUT_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = OUTPUT_RE.exec(line))) {
      if (m.index > last) out.push({ t: line.slice(last, m.index), c: PALETTE.text })
      const c = m[1] || m[3] ? PALETTE.ok : m[2] || (m[4] && !m[4].startsWith('[')) ? PALETTE.warn : m[6] ? PALETTE.bad : PALETTE.muted
      out.push({ t: m[0], c })
      last = m.index + m[0].length
      if (m[0].length === 0) OUTPUT_RE.lastIndex++
    }
    if (last < line.length) out.push({ t: line.slice(last), c: PALETTE.text })
    return out
  })
}

// JSON with cyan keys and emerald strings (MCP tool input / output).
export function highlightJson(value: unknown): Token[] {
  const json = typeof value === 'string' ? value : JSON.stringify(value, null, 2)
  const out: Token[] = []
  const re = /("(?:[^"\\]|\\.)*")(\s*:)?|(-?\d+(?:\.\d+)?|true|false|null)|([^"\d\-tfn]+|.)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(json))) {
    if (m[1]) {
      out.push({ t: m[1], c: m[2] ? PALETTE.type : PALETTE.string })
      if (m[2]) out.push({ t: m[2], c: PALETTE.text })
    } else if (m[3]) {
      out.push({ t: m[3], c: PALETTE.number })
    } else {
      out.push({ t: m[0], c: PALETTE.text })
    }
  }
  return merge(out)
}

function merge(tokens: Token[]): Token[] {
  const out: Token[] = []
  for (const tk of tokens) {
    const prev = out[out.length - 1]
    if (prev && prev.c === tk.c) prev.t += tk.t
    else out.push({ ...tk })
  }
  return out
}
