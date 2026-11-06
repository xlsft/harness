#!/usr/bin/env node
// Extracts the web UI's strings. Run from packages/ui:
//
//   node scripts/i18n-extract.mjs --apply    wraps English text in components and label-like fields in tt(...), then writes the catalog
//   node scripts/i18n-extract.mjs            only (re)writes app/i18n/catalog.json from the tt(...) calls in the code
//   node scripts/i18n-extract.mjs --check    fails when the catalog is stale or a template still has plain English text
//
// English text is the key (app/utils/i18n.ts). What it treats as a string for people:
//   - template text, joined with its {{ interpolations }} into one message with {0}, {1}...;
//   - static attributes people read (label, title, placeholder, aria-label...), and bound ones whose value is a string, a template
//     string, or either inside a ternary or || / ??;
//   - in scripts, the same value in fields people read (label:, title:, description:...) and in toast calls.
// A string starting with a capital letter and holding a letter is copy; identifiers, ids and class names are not.
// It is safe to run again: what is wrapped already is skipped.

import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

// Vue comes with Nuxt here rather than as a dependency of its own.
const nuxt = createRequire(import.meta.url).resolve('nuxt/package.json')
const { MagicString, babelParse, parse, walk } = createRequire(nuxt)('vue/compiler-sfc')

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'app')
const CATALOG = join(root, 'i18n', 'catalog.json')
const mode = process.argv.includes('--apply') ? 'apply' : process.argv.includes('--check') ? 'check' : 'catalog'

const ATTRS = new Set(['label', 'title', 'placeholder', 'aria-label', 'aria-description', 'aria-placeholder', 'alt', 'description', 'hint', 'tooltip', 'message', 'empty', 'heading', 'text-label', 'desc', 'confirm-label', 'cancel-label'])
const FIELDS = new Set(['label', 'title', 'description', 'desc', 'hint', 'placeholder', 'tooltip', 'heading', 'summary', 'emptyText', 'confirmLabel', 'cancelLabel'])
// Text inside these is code or a key name, not copy.
const SKIP_TAGS = new Set(['code', 'pre', 'kbd', 'samp', 'var', 'style', 'script', 'svg', 'HKeys', 'CodePreview', 'XTerm', 'TerminalView'])

const NODE = { ROOT: 0, ELEMENT: 1, TEXT: 2, COMMENT: 3, INTERPOLATION: 5, ATTRIBUTE: 6, DIRECTIVE: 7, COMPOUND: 8 }

const hasLetter = s => /\p{L}/u.test(s)
const isCopy = s => /^\p{Lu}/u.test(s.trim()) && hasLetter(s) && !/^[A-Z0-9_]+$/.test(s.trim()) && !/^[A-Z][a-z]+[A-Z]/.test(s.trim()) && !/[/\\]{1}.*[/\\]/.test(s.trim()) && !/^https?:/.test(s.trim())

// A JS string literal in single quotes.
const js = s => `'${s.replace(/\\/g, '\\\\').replace(/'/g, '\\\'').replace(/\r?\n\s*/g, ' ')}'`

function files(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name !== 'gen' && name !== 'i18n' && name !== 'fixtures') files(p, out)
    } else if (name.endsWith('.vue') || name.endsWith('.ts')) out.push(p)
  }
  return out
}

function parseExpr(code) {
  return babelParse(`(${code})`, { plugins: ['typescript'], sourceType: 'module' }).program.body[0]?.expression
}

// Rewrites `code` (a JS expression) so its copy goes through tt(). Returns null when there is nothing to do.
function wrapExpression(code) {
  let ast
  try {
    ast = parseExpr(code)
  } catch {
    return null
  }
  if (!ast) return null
  const s = new MagicString(`(${code})`)
  let changed = false
  const visit = (n) => {
    if (!n) return
    if (n.type === 'ConditionalExpression') {
      visit(n.consequent)
      visit(n.alternate)
    } else if (n.type === 'LogicalExpression' && (n.operator === '||' || n.operator === '??')) {
      visit(n.left)
      visit(n.right)
    } else if (n.type === 'LogicalExpression') {
      visit(n.right)
    } else if (n.type === 'ParenthesizedExpression') {
      visit(n.expression)
    } else if (wrapLiteral(s, n, `(${code})`)) {
      changed = true
    }
  }
  visit(ast)
  if (!changed) return null
  return s.toString().slice(1, -1)
}

// Wraps a string or template literal node of `src` if it is copy.
function wrapLiteral(s, n, src) {
  if (n.type === 'StringLiteral') {
    if (!isCopy(n.value)) return false
    s.overwrite(n.start, n.end, `tt(${js(n.value)})`)
    return true
  }
  if (n.type === 'TemplateLiteral') {
    const text = n.quasis.map((q, i) => q.value.cooked + (i < n.expressions.length ? `{${i}}` : '')).join('')
    if (!isCopy(text)) return false
    const values = n.expressions.map(e => src.slice(e.start, e.end))
    s.overwrite(n.start, n.end, values.length ? `tt(${js(text)}, [${values.join(', ')}])` : `tt(${js(text)})`)
    return true
  }
  return false
}

// Escapes for an attribute value in double quotes.
const attrValue = s => s.replace(/&/g, '&amp;').replace(/"/g, '&quot;')

function processTemplate(sfc, source, edits, problems, file) {
  const tpl = sfc.descriptor.template
  if (!tpl?.ast) return
  const visit = (node, inSkip) => {
    if (node.type === NODE.ELEMENT) {
      const skip = inSkip || SKIP_TAGS.has(node.tag) || node.props.some(p => p.type === NODE.DIRECTIVE && p.name === 'pre')
      for (const p of node.props) {
        if (skip) break
        if (p.type === NODE.ATTRIBUTE && ATTRS.has(p.name) && p.value && isCopy(p.value.content)) {
          edits.push([p.loc.start.offset, p.loc.end.offset, `:${p.name}="${attrValue(`tt(${js(p.value.content)})`)}"`])
        } else if (p.type === NODE.DIRECTIVE && p.name === 'bind' && p.arg?.content && ATTRS.has(p.arg.content) && p.exp) {
          const code = p.exp.loc.source
          if (code.includes('tt(')) continue
          const out = wrapExpression(code)
          if (out) edits.push([p.exp.loc.start.offset, p.exp.loc.end.offset, attrValue(out).replace(/&amp;&amp;/g, '&&')])
        }
      }
      visitChildren(node, skip)
    } else if (node.children) {
      visitChildren(node, inSkip)
    } else if (node.branches) {
      node.branches.forEach(b => visitChildren(b, inSkip))
    }
  }

  // A run of text and interpolations becomes one message; plain elements between them keep it apart.
  const visitChildren = (parent, skip) => {
    const kids = parent.children ?? []
    let run = []
    const flush = () => {
      if (run.length && !skip) wrapRun(run, source, edits, problems, file)
      run = []
    }
    for (const k of kids) {
      if (k.type === NODE.TEXT || k.type === NODE.INTERPOLATION) run.push(k)
      else {
        flush()
        visit(k, skip)
      }
    }
    flush()
  }
  visit({ type: NODE.ROOT, children: tpl.ast.children }, false)
}

function wrapRun(run, source, edits, problems, file) {
  const texts = run.filter(k => k.type === NODE.TEXT)
  const hasCopy = texts.some(k => hasLetter(k.loc.source))
  if (!hasCopy) {
    // Interpolations alone: a ternary of strings is still copy.
    for (const k of run) {
      if (k.type !== NODE.INTERPOLATION) continue
      const code = k.content.loc.source
      if (code.includes('tt(')) continue
      const out = wrapExpression(code)
      if (out) edits.push([k.content.loc.start.offset, k.content.loc.end.offset, out])
    }
    return
  }
  const first = run[0]
  const last = run[run.length - 1]
  const raw = source.slice(first.loc.start.offset, last.loc.end.offset)
  const lead = raw.match(/^\s*/)[0]
  const trail = raw.match(/\s*$/)[0]
  const values = []
  let message = ''
  for (const k of run) {
    if (k.type === NODE.TEXT) message += k.content
    else {
      message += `{${values.length}}`
      const code = k.content.loc.source.trim()
      values.push(wrapExpression(code) ?? code)
    }
  }
  message = message.replace(/\s+/g, ' ').trim()
  if (!hasLetter(message.replace(/\{\d+\}/g, ''))) return
  // A short lowercase fragment next to punctuation still is copy: anything with a letter here is shown to people.
  if (message.includes('}}') || message.includes('{{')) {
    problems.push(`${file}: cannot wrap: ${message}`)
    return
  }
  const call = values.length ? `tt(${js(message)}, [${values.join(', ')}])` : `tt(${js(message)})`
  edits.push([first.loc.start.offset, last.loc.end.offset, `${lead}{{ ${call} }}${trail}`])
}

// Script strings: values of label-like fields, and toast titles and descriptions (which are `title:` and `description:` fields).
function processScript(content, base, edits) {
  let ast
  try {
    ast = babelParse(content, { plugins: ['typescript'], sourceType: 'module' })
  } catch {
    return
  }
  const s = new MagicString(content)
  walk(ast, {
    enter(n, parent) {
      if (n.type !== 'ObjectProperty' || n.computed || parent?.type !== 'ObjectExpression') return
      const key = n.key.type === 'Identifier' ? n.key.name : n.key.value
      if (!FIELDS.has(key)) return
      const v = n.value
      const visit = (x) => {
        if (x.type === 'ConditionalExpression') {
          visit(x.consequent)
          visit(x.alternate)
        } else {
          wrapLiteral(s, x, content)
        }
      }
      visit(v)
      this.skip?.()
    }
  })
  if (s.hasChanged()) edits.push([base, base + content.length, s.toString()])
}

function applyEdits(source, edits) {
  const sorted = [...edits].sort((a, b) => b[0] - a[0])
  let out = source
  let limit = Infinity
  for (const [from, to, text] of sorted) {
    if (to > limit) continue // Inside something already rewritten.
    out = out.slice(0, from) + text + out.slice(to)
    limit = from
  }
  return out
}

function importLine(content) {
  if (/from '~\/utils\/i18n'/.test(content)) return content
  const line = 'import { tt } from \'~/utils/i18n\'\n'
  const firstImport = content.search(/^import /m)
  return firstImport >= 0 ? content.slice(0, firstImport) + line + content.slice(firstImport) : line + content
}

function collect(content, out) {
  // tt('...') with a plain string: the same quoting js() writes.
  const re = /\btt\(\s*'((?:[^'\\]|\\.)*)'/g
  for (let m = re.exec(content); m; m = re.exec(content)) {
    out.add(m[1].replace(/\\'/g, '\'').replace(/\\\\/g, '\\'))
  }
}

// Text a template still shows without tt(): what --check complains about.
function plainText(file, source) {
  const sfc = parse(source, { filename: file })
  const edits = []
  const problems = []
  processTemplate(sfc, source, edits, problems, file)
  return edits.map(([from, to]) => source.slice(from, to).trim()).filter(Boolean)
}

const catalog = new Set()
const leftovers = []
for (const file of files(root)) {
  let source = readFileSync(file, 'utf8')
  const rel = relative(root, file)
  if (mode === 'apply') {
    const before = source
    if (file.endsWith('.vue')) {
      const sfc = parse(source, { filename: file })
      const edits = []
      const problems = []
      processTemplate(sfc, source, edits, problems, file)
      for (const block of [sfc.descriptor.scriptSetup, sfc.descriptor.script]) {
        if (block) processScript(block.content, block.loc.start.offset, edits)
      }
      for (const p of problems) console.warn(p)
      source = applyEdits(source, edits)
    } else if (rel.startsWith('lib/') && !rel.startsWith('lib/transport/mock') && !rel.includes('scenario') && !rel.endsWith('/mock.ts')) {
      const edits = []
      processScript(source, 0, edits)
      if (edits.length) source = importLine(applyEdits(source, edits))
    }
    if (source !== before) writeFileSync(file, source)
  }
  collect(source, catalog)
  if (mode === 'check' && file.endsWith('.vue')) {
    for (const text of plainText(file, source)) leftovers.push(`${rel}: ${text.slice(0, 100)}`)
  }
}

const sorted = [...catalog].sort((a, b) => a.localeCompare(b, 'en'))
const json = `${JSON.stringify(sorted, null, 2)}\n`
if (mode === 'check') {
  let current = ''
  try {
    current = readFileSync(CATALOG, 'utf8')
  } catch { /* no catalog yet: stale */ }
  const stale = current !== json
  if (stale) console.error('app/i18n/catalog.json is stale: run node scripts/i18n-extract.mjs')
  if (leftovers.length) console.error(`Templates with plain text:\n${leftovers.join('\n')}`)
  process.exit(stale || leftovers.length ? 1 : 0)
}
writeFileSync(CATALOG, json)
console.log(`${sorted.length} strings in app/i18n/catalog.json`)
