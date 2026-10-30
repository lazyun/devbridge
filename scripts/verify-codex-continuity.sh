#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
native="${DEVBRIDGE_PLATFORM_NATIVE_BINARY:-}"
if [[ "$native" != /* || ! -x "$native" ]]; then
  printf 'Set DEVBRIDGE_PLATFORM_NATIVE_BINARY to the absolute installed Codex 0.159.2 native executable.\n' >&2
  exit 2
fi
if [[ "$("$native" --version)" != 'codex-cli 0.159.2' ]]; then
  printf 'This continuity acceptance gate requires exactly codex-cli 0.159.2.\n' >&2
  exit 2
fi
evidence_parent="${DEVBRIDGE_CONTINUITY_EVIDENCE_DIR:-$repo_root/build/codex-continuity-01592-2026-10-02}"
mkdir -p "$evidence_parent"
evidence_dir="$(mktemp -d "$evidence_parent/gate.XXXXXX")"
evidence_dir="$(cd "$evidence_dir" && pwd)"
export DEVBRIDGE_CONTINUITY_EVIDENCE_DIR="$evidence_dir"
export GOCACHE="${GOCACHE:-$repo_root/build/remaining-constraints-2026-10-02/go-cache}"
printf 'Native continuity evidence: %s\n' "$evidence_dir"

# Only model Responses are synthetic. The selected native owns each real
# App Server/Thread/Turn. A consumed answer without its ack stays UNKNOWN.
for scenario in waiting recorded consumed multi inflight; do
  python3 scripts/probe_codex_continuity.py --executable "$native" \
    --output "$evidence_dir/native-$scenario" --scenario "$scenario" \
    2>&1 | tee "$evidence_dir/native-$scenario.log"
done

# Includes version pinning and both direct and symlink Unix endpoint fencing.
go test ./internal/appserver -count=1 -v \
  2>&1 | tee "$evidence_dir/appserver.log"

go test ./internal/execution \
  -run '^TestQualified(AppServerRecovery|Recovery)' -count=1 -v \
  2>&1 | tee "$evidence_dir/qualified-recovery.log"
go test ./internal/storage/sqlite \
  -run '^(TestTask(RecoveryBootReconciler|Continuity)|TestRuntimeWorkset(AtomicBindingAndExactReplay|BindingRejectsScopeExpansion)V90|TestRuntimePreparationCreateRunRejectsTopologyDriftV90)' \
  -count=1 -v 2>&1 | tee "$evidence_dir/recovery-store.log"

# Real formal qualification and binding, public start, and exact original-turn
# recovery after graceful Core reopen and forced termination of an owned Core.
go test ./internal/controlapi \
  -run '^TestRuntimeContinuityQualifiedNativeCore(Reopen|Crash)$' \
  -count=1 -timeout 5m -v 2>&1 | tee "$evidence_dir/core-native.log"
