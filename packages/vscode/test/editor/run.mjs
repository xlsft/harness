#!/usr/bin/env node
// Runs test/editor/suite.cjs in a real VS Code, against a real harness-client and the fake gateway: no account, no key, no
// model. It needs `node scripts/build.mjs --ui` first, and go. The editor is downloaded into .vscode-test here, which is
// git-ignored; remove it when disk is short. Set HARNESS_VSCODE_VERSION to try another version.
import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync, realpathSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { runTests } from '@vscode/test-electron'

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const clientDir = join(root, '..', 'client')
if (!existsSync(join(root, 'media', 'ui', 'index.html'))) throw new Error('media/ui is missing: run node scripts/build.mjs --ui')

const work = realpathSync(mkdtempSync(join(tmpdir(), 'harness-vscode-e2e-')))
const bin = join(work, 'bin')
const workspace = join(work, 'workspace')
const home = join(work, 'home')
for (const d of [bin, workspace, home, join(work, 'tmp')]) mkdirSync(d, { recursive: true })

console.log('building harness-client and the fake gateway')
execFileSync('go', ['build', '-o', join(bin, 'harness-client'), './cmd/harness-client'], { cwd: clientDir, stdio: 'inherit' })
execFileSync('go', ['build', '-o', join(bin, 'fakegateway'), './internal/fakegateway/cmd/fakegateway'], { cwd: clientDir, stdio: 'inherit' })

const gateway = spawn(join(bin, 'fakegateway'), ['-addr', '127.0.0.1:0', '-scenario', 'ide'], { stdio: ['ignore', 'pipe', 'inherit'] })
const gatewayUrl = await new Promise((resolve, reject) => {
  let out = ''
  gateway.stdout.on('data', (chunk) => {
    out += chunk
    const m = /listening on (\S+)/.exec(out)
    if (m) resolve(m[1])
  })
  gateway.on('exit', code => reject(new Error(`the fake gateway exited: ${code}`)))
})

let failed = 0
try {
  failed = await runTests({
    version: process.env.HARNESS_VSCODE_VERSION || '1.100.0',
    cachePath: join(root, '.vscode-test'),
    extensionDevelopmentPath: root,
    extensionTestsPath: join(root, 'test', 'editor', 'suite.cjs'),
    launchArgs: [workspace, '--disable-extensions', '--disable-workspace-trust', '--disable-gpu', '--skip-welcome', '--skip-release-notes', `--user-data-dir=${join(work, 'user-data')}`],
    extensionTestsEnv: {
      PATH: `${bin}${delimiter}${process.env.PATH}`,
      HARNESS_HOME: home,
      HARNESS_TMPDIR: join(work, 'tmp'),
      HARNESS_KEYRING: '0',
      HARNESS_NO_SHELL_SNAPSHOT: '1',
      HARNESS_NO_HOOKS: '1',
      HARNESS_ACCESS_TOKEN: 'hat_e2e',
      HARNESS_SERVER_URL: gatewayUrl,
      HARNESS_TEST_WORKSPACE: workspace
    }
  })
} catch (err) {
  console.error(err)
  failed = 1
} finally {
  gateway.kill()
  rmSync(work, { recursive: true, force: true, maxRetries: 5 })
}
process.exit(failed ? 1 : 0)
