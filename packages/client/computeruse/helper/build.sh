#!/usr/bin/env bash
# Compiles the macOS computer use helper: build.sh <output path> [swift target triple]. Needs only the Xcode command line
# tools, which a macOS Rust or Tauri build needs anyway. The client looks for the program next to its own executable, or
# in HARNESS_HOME/bin, so for a dev client run: build.sh ~/.harness/bin/harness-computer-helper
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
out="${1:?usage: build.sh <output path> [target triple]}"
mkdir -p "$(dirname "$out")"
target=()
[[ -n "${2:-}" ]] && target=(-target "$2")
swiftc -O -swift-version 5 ${target[@]+"${target[@]}"} -o "$out" "$here/main.swift"
