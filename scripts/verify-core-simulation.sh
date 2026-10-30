#!/usr/bin/env bash
# Model-free integration through real packaged Core/MCP ingress. Never connects
# to a user's existing service and never edits the source checkout's Git state.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
parent="${DEVBRIDGE_SIM_EVIDENCE_DIR:-${TMPDIR:-/tmp}}"
mkdir -p "$parent"
parent="$(cd -- "$parent" && pwd)"
build_dir="$(mktemp -d "$parent/devbridge-sim-build.XXXXXX")"
mkdir -p "$build_dir/bin"
printf 'Build evidence: %s\n' "$build_dir"
git rev-parse HEAD > "$build_dir/source-revision.txt"
git status --short > "$build_dir/source-status.txt"
go build -trimpath -o "$build_dir/bin/" ./cmd/devbridge ./cmd/devbridge-mcp ./cmd/devbridge-local-mcp ./cmd/devbridge-sim
"$build_dir/bin/devbridge-sim" -bin-dir "$build_dir/bin" -evidence-dir "$build_dir" "$@" 2>&1 | tee "$build_dir/simulation.log"
