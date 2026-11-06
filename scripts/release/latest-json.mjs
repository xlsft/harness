#!/usr/bin/env node
// Writes the Tauri updater's latest.json for a release from the signed archives in a directory. An archive without its
// signature fails the run: an updater would refuse it anyway, after the release is out.
//
// Usage: latest-json.mjs <tag> <directory> <notes file>   (GITHUB_REPOSITORY names the repository; default xlsft/harness)
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const [tag, dir, notesFile] = process.argv.slice(2)
if (!tag || !dir || !notesFile) {
  console.error('usage: latest-json.mjs <tag> <directory> <notes file>')
  process.exit(1)
}
const version = tag.replace(/^v/, '')
const repo = process.env.GITHUB_REPOSITORY || 'xlsft/harness'

const platforms = {}
for (const [platform, arch] of [['darwin-aarch64', 'arm64'], ['darwin-x86_64', 'amd64']]) {
  const name = `harness_${version}_darwin_${arch}.app.tar.gz`
  if (!existsSync(join(dir, name))) continue
  if (!existsSync(join(dir, `${name}.sig`))) {
    console.error(`${name} has no signature`)
    process.exit(1)
  }
  platforms[platform] = {
    signature: readFileSync(join(dir, `${name}.sig`), 'utf8').trim(),
    url: `https://github.com/${repo}/releases/download/${tag}/${name}`
  }
}
if (!platforms['darwin-aarch64']) {
  console.error(`no harness_${version}_darwin_arm64.app.tar.gz in ${dir}`)
  process.exit(1)
}
writeFileSync(join(dir, 'latest.json'), `${JSON.stringify({
  version,
  notes: readFileSync(notesFile, 'utf8').trim(),
  pub_date: new Date().toISOString(),
  platforms
}, null, 2)}\n`)
