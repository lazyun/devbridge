#!/usr/bin/env bash
# Explicit test-only composition, NOT a production native-qualification gate.
# Real Core plus trusted deterministic Agent; never connects to existing Core.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
hash_cmd=(sha256sum)
if ! command -v sha256sum >/dev/null 2>&1; then hash_cmd=(shasum -a 256); fi
build_flags=()
scenario=Gate
usage() { echo "Usage: $0 [--race] [--sequential | --response-loss | --cross-session | --journal-unknown | --journal-recovery | --continue-unknown | --submission-withdraw | --withdraw-arbitration | --cancel-late-completion | --process-crash]" >&2; exit 2; }
while (($#)); do
  case "$1" in
    --race) ((${#build_flags[@]} == 0)) || usage; build_flags=(-race) ;;
    --sequential) [[ "$scenario" == Gate ]] || usage; scenario=SequentialAnomalies ;;
    --response-loss) [[ "$scenario" == Gate ]] || usage; scenario=ResponseLoss ;;
    --cross-session) [[ "$scenario" == Gate ]] || usage; scenario=CrossSession ;;
    --journal-unknown) [[ "$scenario" == Gate ]] || usage; scenario=JournalUnknown ;;
    --journal-recovery) [[ "$scenario" == Gate ]] || usage; scenario=JournalRecovery ;;
    --continue-unknown) [[ "$scenario" == Gate ]] || usage; scenario=ContinueUnknown ;;
    --submission-withdraw) [[ "$scenario" == Gate ]] || usage; scenario=SubmissionWithdraw ;;
    --withdraw-arbitration) [[ "$scenario" == Gate ]] || usage; scenario=WithdrawArbitration ;;
    --cancel-late-completion) [[ "$scenario" == Gate ]] || usage; scenario=CancelLateCompletion ;;
    --process-crash) [[ "$scenario" == Gate ]] || usage; scenario=ProcessCrash ;;
    *) usage ;;
  esac
  shift
done
parent="${DEVBRIDGE_SIM_EVIDENCE_DIR:-${TMPDIR:-/tmp}}"
mkdir -p "$parent"
parent="$(cd -- "$parent" && pwd)"
root="$(mktemp -d "$parent/devbridge-test-only-continuation.XXXXXX")"
printf 'TEST-ONLY trusted Agent/platform composition; evidence: %s\n' "$root"
printf '%s\n' "$scenario" > "$root/scenario.txt"
git rev-parse HEAD > "$root/source-revision.txt"
git status --short > "$root/source-status.txt"
git diff --binary > "$root/tracked-source-diff.patch"
go version > "$root/go-version.txt"
"${hash_cmd[@]}" internal/controlapi/simulated_continuation_gate_test.go \
  internal/controlapi/simulated_continuation_response_loss_test.go \
  internal/controlapi/simulated_continuation_cross_session_test.go \
  internal/controlapi/simulated_continuation_journal_unknown_test.go \
  internal/controlapi/simulated_continuation_journal_recovery_test.go \
  internal/controlapi/simulated_continuation_continue_unknown_test.go \
  internal/controlapi/simulated_continuation_submission_withdraw_test.go \
  internal/controlapi/simulated_continuation_withdraw_arbitration_test.go \
  internal/controlapi/simulated_continuation_cancel_late_test.go \
  internal/controlapi/simulated_continuation_process_crash_test.go \
  internal/controlapi/protocol_platform_fixture_test.go \
  internal/controlapi/multi_repository_coding_admission_test.go \
  internal/controlapi/multi_repository_upper_lifecycle_test.go \
  scripts/test-simulated-continuation.sh > "$root/source.sha256"
go test "${build_flags[@]}" -c -o "$root/continuation.test" ./internal/controlapi
"${hash_cmd[@]}" "$root/continuation.test" > "$root/binary.sha256"
# Refuse a build-tag-excluded or misspelled selector rather than passing only
# PreparingReceipt (for example --process-crash on an unsupported platform).
"$root/continuation.test" -test.list="^TestSimulatedContinuation${scenario}$" > "$root/scenario-discovery.txt"
if ! grep -Fxq "TestSimulatedContinuation${scenario}" "$root/scenario-discovery.txt"; then
  echo "Selected scenario is not present in this platform's test binary: $scenario" >&2
  printf '2\n' > "$root/test.exit"
  exit 2
fi
set +e
DEVBRIDGE_SIMULATED_CONTINUATION=1 DEVBRIDGE_CONTINUATION_EVIDENCE_DIR="$root" \
  "$root/continuation.test" -test.run="^TestSimulatedContinuation(${scenario}|PreparingReceipt)$" -test.v -test.timeout=8m \
  2>&1 | tee "$root/test.log"
status=${PIPESTATUS[0]}
set -e
printf '%s\n' "$status" > "$root/test.exit"
"${hash_cmd[@]}" -c "$root/source.sha256" > "$root/source-after.txt"
exit "$status"
