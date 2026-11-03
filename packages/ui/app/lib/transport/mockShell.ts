// The mock transport's user shell: a tiny interpreter for the composer's shell mode, so the shell card can be
// reached without a client. It knows echo, pwd, cd, ls, sleep, true / false and exit; anything else fails the
// way sh does. Commands may be joined with && and ;.

export interface MockShellResult {
  stdout: string
  stderr: string
  exitCode: number
  // The shell directory after the command (cd).
  cwd: string
  // How long the command runs (sleep), in ms.
  durationMs: number
}

function resolve(cwd: string, dir: string) {
  if (!dir || dir === '~') return cwd.split('/').slice(0, 3).join('/') || '/'
  const parts = (dir.startsWith('/') ? dir : `${cwd}/${dir}`).split('/')
  const out: string[] = []
  for (const p of parts) {
    if (!p || p === '.') continue
    if (p === '..') out.pop()
    else out.push(p)
  }
  return '/' + out.join('/')
}

function unquote(s: string) {
  return s.replace(/^(['"])(.*)\1$/, '$2')
}

export function runMockShell(command: string, cwd: string): MockShellResult {
  const r: MockShellResult = { stdout: '', stderr: '', exitCode: 0, cwd, durationMs: 40 }
  // Split on ; and && (an && after a failure skips the rest of that chain).
  const steps = command.split(/\s*(;|&&)\s*/)
  let skip = false
  for (let i = 0; i < steps.length; i += 2) {
    const line = steps[i]!.trim()
    const sep = steps[i - 1]
    if (sep === ';') skip = false
    if (skip || !line) continue
    const [name = '', ...args] = line.split(/\s+/)
    const arg = unquote(args.join(' '))
    switch (name) {
      case 'echo': {
        const m = line.match(/^echo\s+(.*?)\s*(>&2|1>&2)?$/)
        const text = unquote(m?.[1] ?? '') + '\n'
        if (m?.[2]) r.stderr += text
        else r.stdout += text
        r.exitCode = 0
        break
      }
      case 'pwd':
        r.stdout += r.cwd + '\n'
        r.exitCode = 0
        break
      case 'cd':
        r.cwd = resolve(r.cwd, arg)
        r.exitCode = 0
        break
      case 'ls':
        r.stdout += ['README.md', 'package.json', 'src'].join('\n') + '\n'
        r.exitCode = 0
        break
      case 'sleep':
        r.durationMs += (Number(arg) || 0) * 1000
        r.exitCode = 0
        break
      case 'true':
        r.exitCode = 0
        break
      case 'false':
        r.exitCode = 1
        break
      case 'exit':
        r.exitCode = Number(arg) || 0
        return r
      default:
        r.stderr += `sh: ${name}: command not found\n`
        r.exitCode = 127
    }
    if (r.exitCode !== 0 && steps[i + 1] === '&&') skip = true
  }
  return r
}
