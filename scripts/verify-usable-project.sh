#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
if [[ -z "${DEVBRIDGE_PLATFORM_NATIVE_BINARY:-}" || ! -x "$DEVBRIDGE_PLATFORM_NATIVE_BINARY" ]]; then
  printf 'Set DEVBRIDGE_PLATFORM_NATIVE_BINARY to an installed native Codex executable. This gate uses only a local Responses provider.\n' >&2
  exit 2
fi
evidence_parent="${DEVBRIDGE_USABLE_PROJECT_EVIDENCE_DIR:-$repo_root/build/remaining-constraints-2026-10-02}"
mkdir -p "$evidence_parent"
evidence_dir="$(mktemp -d "$evidence_parent/usable-project.XXXXXX")"
export GOCACHE="${GOCACHE:-$repo_root/build/remaining-constraints-2026-10-02/go-cache}"

printf 'Build and workflow evidence: %s\n' "$evidence_dir"
mkdir -p "$evidence_dir/bin"
go build -trimpath -o "$evidence_dir/bin/" ./cmd/...
DEVBRIDGE_USABLE_PROJECT_BIN="$evidence_dir/bin" \
DEVBRIDGE_USABLE_PROJECT_EVIDENCE_DIR="$evidence_dir" \
  go test ./internal/localmcp -run '^TestUsableProjectCandidatePublicLifecycle$' -count=1 -timeout 6m -v \
  2>&1 | tee "$evidence_dir/acceptance.log"
