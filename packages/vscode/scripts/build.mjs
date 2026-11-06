#!/usr/bin/env node
// Builds the extension. Plain: bundles src/ into dist/extension.cjs. --ui: also builds the panel from packages/ui into
// media/ui (needs `pnpm install` in packages/ui). --vsix: also packs dist/harness-<version>.vsix from what media/ui holds. Nothing here publishes.
import { spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const repo = join(root, '..', '..')
const ui = join(repo, 'packages', 'ui')
const args = new Set(process.argv.slice(2))

function run(command, argv, options) {
  const r = spawnSync(command, argv, { stdio: 'inherit', ...options })
  if (r.status !== 0) throw new Error(`${command} ${argv.join(' ')} failed with ${r.status ?? r.signal}`)
}

await build({
  entryPoints: [join(root, 'src', 'extension.ts')],
  outfile: join(root, 'dist', 'extension.cjs'),
  bundle: true,
  platform: 'node',
  format: 'cjs',
  target: 'node20',
  external: ['vscode'],
  sourcemap: true,
  logLevel: 'warning'
})

if (args.has('--ui')) {
  run('pnpm', ['exec', 'nuxt', 'generate'], { cwd: ui, env: { ...process.env, HARNESS_UI_TARGET: 'ide' } })
  const media = join(root, 'media', 'ui')
  rmSync(media, { recursive: true, force: true })
  mkdirSync(media, { recursive: true })
  const from = join(ui, '.output', 'public')
  for (const name of ['index.html', '_nuxt', 'fonts']) cpSync(join(from, name), join(media, name), { recursive: true })
  // The output is a build for this one target; a later plain UI build must not find it.
  rmSync(join(ui, '.output'), { recursive: true, force: true })
  rmSync(join(ui, '.nuxt'), { recursive: true, force: true })
  run('pnpm', ['exec', 'nuxt', 'prepare'], { cwd: ui })
}

if (args.has('--vsix')) {
  if (!existsSync(join(root, 'media', 'ui', 'index.html'))) throw new Error('media/ui is missing: add --ui')
  const { version } = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))
  mkdirSync(join(root, 'dist'), { recursive: true })
  cpSync(join(repo, 'LICENSE'), join(root, 'LICENSE'))
  try {
    run('pnpm', ['exec', 'vsce', 'package', '--no-dependencies', '--skip-license', '--out', join('dist', `harness-${version}.vsix`)], { cwd: root })
  } finally {
    rmSync(join(root, 'LICENSE'), { force: true })
  }
}
