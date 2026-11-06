#!/usr/bin/env node
// Writes a release's version into the packages that carry one, in the workflow's checkout only: nothing is committed, and
// the tag stays the source of truth. The client gets it through -ldflags and core through a build arg instead.
//
// Usage: stamp.mjs <version> [repository root]
import { readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const [version, root = resolve(import.meta.dirname, '../..')] = process.argv.slice(2)
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version ?? '')) {
  console.error(`"${version}" is not a version: expected X.Y.Z or X.Y.Z-rc.N`)
  process.exit(1)
}

function edit(path, change) {
  const file = join(root, path)
  const before = readFileSync(file, 'utf8')
  const after = change(before)
  if (after === before && !before.includes(version)) throw new Error(`${path}: found no version to stamp`)
  writeFileSync(file, after)
}

const json = text => {
  const data = JSON.parse(text)
  data.version = version
  return `${JSON.stringify(data, null, 2)}\n`
}
for (const path of ['packages/core/package.json', 'packages/ui/package.json', 'packages/desktop/package.json', 'packages/desktop/src-tauri/tauri.conf.json']) edit(path, json)
edit('packages/desktop/src-tauri/Cargo.toml', text => text.replace(/^version = ".*"$/m, `version = "${version}"`))
edit('packages/desktop/src-tauri/Cargo.lock', text => text.replace(/(name = "harness-desktop"\nversion = )".*"/, `$1"${version}"`))
console.log(`stamped ${version}`)
