# Workflow acceptance and historical real Codex gates

## Current packaged workflow gate

Select an installed **native Codex executable**, then run from the repository
root. The script requires `DEVBRIDGE_PLATFORM_NATIVE_BINARY`; an omitted or
non-executable path exits with status 2 before building:

```sh
DEVBRIDGE_PLATFORM_NATIVE_BINARY=/absolute/path/to/native/codex \
  ./scripts/verify-usable-project.sh
```

The script builds Core and both adapters, then runs
`TestUsableProjectCandidatePublicLifecycle` through their public HTTP/MCP
interfaces. It uses random loopback ports and a fresh DataRoot, creates three
disposable Git repositories, and registers the Project through the production
API. The workflow covers Project/branch selection, FeatureArea and Task creation,
Upper Review with file evidence, approve-only, an explicit Local MCP model/backend
selection, implementation, verification, acceptance, Git commit, FeatureArea
merge and replay after all three processes restart. Independent Git assertions
check both selected repositories and the unchanged third repository.

The gate launches that **real native App Server** against a deterministic
loopback Responses provider, with isolated HOME/CODEX_HOME and disposable
configuration. It does not use a fake App Server or an online model provider.
Implementation edits and verification commands execute in the native runtime;
this is not an online model-quality, provider-availability, or real-user
confirmation test. The script retains logs, protocol messages and disposable
repositories at its printed evidence path.

Before implementation, the test stops the services and invokes the same
packaged Core's `-mode qualify-runtime` against the fixture DataRoot. The
[native collector](../../internal/platformcollector/provider.go) currently
recognizes `codex-cli 0.149.0` and `codex-cli 0.159.2`; version recognition alone
does not establish qualification. The native artifact, protocol and runtime
checks must also pass. Separate App Server version admission elsewhere does
not override this gate's collector policy.

The host must support the native runtime's filesystem sandbox. An enclosing
Linux sandbox can prevent nested execution, for example by exposing the fixed
`/tmp/codex-daemon-<uid>` directory as a read-only mode-000 mount while the native
runtime requires mode 0700. That is an environment blocker, not by itself a
Dev Bridge workflow defect. Do not change protected mounts or permissions,
disable the sandbox, or spoof the native version to make the gate pass. Retain
the exact qualification error and report which stages were not reached.

For the test alone after a build:

```sh
DEVBRIDGE_PLATFORM_NATIVE_BINARY=/absolute/path/to/native/codex \
DEVBRIDGE_USABLE_PROJECT_BIN=/absolute/path/to/bin \
  go test ./internal/localmcp -run '^TestUsableProjectCandidatePublicLifecycle$' -count=1 -timeout 6m -v
```

`DEVBRIDGE_USABLE_PROJECT_BIN` must contain Core and both adapters from the same
build. Without it, the test deliberately skips; with it set but no native
selection, the test fails. The script supplies freshly built binaries and
retained evidence automatically. For a direct test invocation, optionally set
`DEVBRIDGE_USABLE_PROJECT_EVIDENCE_DIR` to an existing directory to retain the
otherwise temporary fixtures. A native qualification refusal is a test failure,
not a skip or a completed workflow acceptance.

## Historical real Codex harness

The harness below predates the current purpose-confirmation catalog. Its adapter
still uses retired entry points, including direct Task creation and old UI
decision tools. It is retained as historical test design and must not be used as
proof that the current product workflow passes. Migration of that online harness
is separate from the current offline packaged gate above.

This harness runs Dev Bridge against a real `codex app-server`. A gate is a
`PASS` only when a black-box adapter invokes a real Dev Bridge executable or
public API backed by a real app-server process and all independent assertions
succeed.

The harness always creates a disposable source repository. Never point a gate
at the Dev Bridge source checkout. The fixture has a deterministic initial
commit, tracked fixture files plus a README, a clean source checkout, an
isolated DataRoot, and an artifact directory. It can create one or
two independent Git worktrees from the same immutable base commit.

## Run it

Build the production Bridge and MCP executables and emit the nine
machine-readable gate results through the real loopback adapter:

```bash
go build -o /tmp/devbridge-real-e2e ./cmd/devbridge
go build -o /tmp/devbridge-real-e2e-mcp ./cmd/devbridge-mcp
go run ./test/real_e2e/cmd/gate \
  -bridge /tmp/devbridge-real-e2e \
  -mcp /tmp/devbridge-real-e2e-mcp \
  -codex /absolute/path/to/codex \
  -require-pass
```

Run only the M5-B work/verification gate with `-gate REAL-E2E-07`.
Run only the execution-scope gate with `-gate REAL-E2E-08`.
Run only the FeatureArea Review and explicit Task gate with `-gate REAL-E2E-09`.

`-codex` is optional when `codex` is already on `PATH`. If `-bridge` (or
`DEVBRIDGE_REAL_E2E_BRIDGE`) is omitted, the command intentionally emits
`BLOCKED` instead of substituting the smoke CLI or a fake transport.
REAL-E2E-09 also require `-mcp` (or
`DEVBRIDGE_REAL_E2E_MCP`) so their card continuations and external façade calls
cross the production MCP boundary.

Useful options:

- `-root /safe/disposable/path` chooses the fixture, state, and artifact root.
- `-keep` retains artifacts even after a fully successful run.
- `-timeout 15m` sets the suite deadline.
- `-bridge /absolute/path` selects the production executable.
- `-mcp /absolute/path` selects the production MCP executable for REAL-E2E-09.
- `-codex /absolute/path` selects the real Codex launcher.

The output is a JSON array of objects with `gate_id`, `status`, `reason`,
`evidence`, `started_at`, and `finished_at`. Valid statuses are `PASS`, `FAIL`,
`SKIPPED`, and `BLOCKED`. A missing capability must never be represented as a
pass.

## Black-box adapter contract

Implement `harness.Adapter` using only a stable Dev Bridge executable or public
API. The adapter receives the disposable source path, immutable base commit,
expected Bridge-owned worktree root, DataRoot, artifact directory,
and exact requested file mutation. It reports the externally observed worktree
path, IDs, and lifecycle checkpoints. The harness, not the adapter or agent
text, performs the final Git and filesystem assertions.

`RealAdapter` uses only loopback HTTP and child-process operations. It does not
import the coordinator, SQLite stores, domain models, or Codex gateway.

The production executable's server mode accepts one DataRoot and only an
explicit loopback IP:

```bash
devbridge -mode server \
  -data-root /safe/devbridge-data \
  -listen 127.0.0.1:8081 \
  -admin-test
```

## Gate acceptance criteria

### REAL-E2E-01 — normal real Codex execution

All of the following are required:

1. The adapter runs Dev Bridge with a real `codex app-server` process.
2. The source repository has the same HEAD, status, and diff before and after.
3. Execution occurs only in the fixture's Bridge-owned worktree.
4. The expected file has the exact requested content in that worktree.
5. The observed lifecycle orders Codex completion before evidence collection,
   and evidence collection before Run success.
6. An independent Git inspection finds the expected changed file and diff.
7. The agent's final text is retained only as a claim, never as Git proof.

The harness additionally compares persisted BaseCommit, HeadCommit, Dirty,
changed files, and bounded diff bytes with its independent Git inspection.

### REAL-E2E-02 — runtime approval

All of the following are required:

1. A Run reaches `WAITING_APPROVAL` through a real runtime request.
2. The approval has stable ownership and correlation to the observed Run.
3. A `DECLINE` decision can be supplied through a public endpoint.
4. The timeline records `PENDING`, `DECIDED`, and `DELIVERED` as distinct
   states and records waiting before decline.
5. Declining does not automatically transition the Run to a failure status.

### REAL-E2E-03 — interruption while active

All of the following are required:

1. The adapter supplies a long-running Dev Bridge child-process command.
2. The harness observes the Run as active, then kills the Bridge process
   non-gracefully.
3. The harness restarts Bridge with the exact same DataRoot.
4. The adapter observes a definite recovered terminal/reconciliation result.
5. Both process generations' stdout and stderr paths are included as evidence.
6. Recovery records `bridge_restart_interrupted`, retains the worktree, does
   not recreate a live session or token usage, and is idempotent on a second
   restart.

### REAL-E2E-04 — interruption while waiting for approval

All of the following are required:

1. A real Run is externally observed in `WAITING_APPROVAL`.
2. The harness kills Bridge non-gracefully and restarts it with the same state
   root.
3. The restarted process exposes the recovered Run/Approval relationship and a
   definite recovery status.
4. Process logs and DataRoot location are preserved as evidence.
5. A pending approval becomes `EXPIRED`, and the recovered result is unchanged
   by a second restart. Store-level recovery tests also cover preservation of a
   recorded decision and the immutability of already delivered approvals.

### REAL-E2E-05 — parallel Task isolation

All of the following are required:

1. Two Tasks in different ACTIVE FeatureAreas of one Project and Repository own different Runs and worktrees; their FeatureArea AgentConversations own distinct real Codex threads.
2. Run A remains in `WAITING_APPROVAL` while Run B executes and succeeds.
3. A second Agent operation in FeatureArea A is rejected by the durable FeatureArea claim, even when it belongs to another Task or Review.
4. Per-Run token status and Evidence remain isolated.

### REAL-E2E-06 — Workspace and Codex Thread continuity

All of the following are required:

1. Run 1 succeeds for a newly created Task and records nonzero token usage.
2. Bridge is stopped and restarted with the same DataRoot.
3. A second confirmed ExecutionProposal for the exact same applied TaskPlan starts a distinct Run 2 for the same Task.
4. Both Runs report the same nonempty Workspace, worktree, worktree path, and Codex thread IDs, but different nonempty turn IDs.
5. Run 1's end Git tree equals Run 2's start Git tree.
6. Run 1 evidence contains only its `tracked.txt` mutation; repeating the exact plan makes Run 2's delta empty, while the cumulative dirty worktree still contains only `tracked.txt`.
7. Both per-Run token deltas are `AVAILABLE` and nonzero, proving the persisted cumulative baseline was used after cold resume.

### REAL-E2E-07 — evidence-driven implementation and verification planning

The gate uses real Codex traffic for three separately confirmed plans:

1. A `RUN` plan creates `IMPLEMENTATION` first and `VERIFICATION` only after
   implementation has durably succeeded.
2. Both Runs link to one ExecutionProposal and one Task, Workspace, worktree,
   and Codex thread, while retaining distinct Run and turn IDs.
3. Start/finish timestamps and nonzero `AVAILABLE` token deltas are recorded
   independently for both Runs.
4. The frozen scope and `GIT_ASSERTION` command remain readable. Codex executes
   `git diff --check` through a real App Server command item; Bridge matches the
   declared command to the protocol-provided command action, records the raw
   shell wrapper and real exit code 0, and derives PASS without parsing prose.
5. The implementation end snapshot equals the verification start snapshot,
   and verification start/end snapshots are identical with no verification
   changed files.
6. Task/execution success is observed only with successful verification, and
   the disposable source checkout remains unchanged.
7. A controlled real `false` command exits nonzero, producing a FAILED check,
   Verification Run, execution outcome, and Task from Evidence even though a
   natural-language AgentClaim is retained independently.
8. A separate `NONE` / `USER_WAIVED` plan succeeds with exactly one
   `IMPLEMENTATION` Run, no verification Run ID, a durable waiver reason, and
   verification cost reported as `NOT_REQUESTED` rather than zero-token work.

Neither deterministic case requires a Bridge marker, JSON footer, or structured
final prose.

### REAL-E2E-08 — implementation ExecutionScope

The gate uses a disposable repository containing `allowed/target.txt`, an
executable deterministic mutation fixture under `allowed/`, and
`outside/protected.txt`, and drives real Codex traffic for three cases:

1. `PATHS ["allowed"]` permits only the target under that directory; the
   implementation and its confirmed verification both succeed with exact
   per-Run evidence.
2. A deliberate mutation of both files under the same PATHS contract is
   captured by Git Evidence, fails as `OUT_OF_SCOPE_CHANGE`, reports the
   offending path, retains the dirty worktree without reverting it, and never
   reserves the otherwise-confirmed Verification Run. If real Codex requests
   a `FILE_CHANGE` or `COMMAND_EXECUTION` protocol approval for the exact
   disposable fixture execution, the harness grants only that pending one-time
   action through the public approval API; the confirmed WorkSpec remains
   unchanged and Bridge's scope check must still reject the resulting delta.
3. Explicit `REPOSITORY` authorizes changes in both directories; omitting paths
   alone is never interpreted as repository-wide authorization.

### REAL-E2E-09 — FeatureArea Review and explicit Task continuity

The gate uses real Codex, the public loopback API, and the production MCP
service to assert the ownership closure product contract (the live gate must be updated before its next live run):

## Failure artifacts

On `FAIL` or `BLOCKED`, a temporary suite root is retained and printed to
stderr. Gate evidence records the fixture repository, base commit, DataRoot,
artifact root, and—when available—the worktree path, Git status/diff, Run ID,
recovery status, and child stdout/stderr logs. A fully passing or skipped
temporary run is removed by default; `-keep` overrides cleanup.

## Foundation verification

```bash
go test ./test/real_e2e/...
go test -race ./test/real_e2e/...
go test ./...
go vet ./...
git diff --check
```

### REAL-E2E-06 through REAL-E2E-09 — retained execution-safety gates

REAL-E2E-06 through REAL-E2E-09 retain their established execution-safety
semantics. Where their black-box adapter formerly selected low-level MCP
primitives, it must use the equivalent external Project → Feature Area → Task →
Execution façade. Removing a model-visible primitive is not grounds to weaken a
gate: durable audit, authorization, approval, recovery, Git/Evidence, and
isolation assertions remain applicable. Internal services may still be tested
directly for their technical invariants.

### Current approved workflow candidate gates

The old external workflow and Task handoff gates (10/11) were removed with their
obsolete stage contracts. Build Core, GPT MCP and Local MCP, then use the
`internal/localmcp` candidate tests for Review Approval, Web observability,
unified cancellation and retirement. These run real packaged processes with a
deterministic fake app-server, never paid Codex turns. Their environment variables
are `DEVBRIDGE_REVIEW_APPROVAL_V2_BIN`, `DEVBRIDGE_WEB_OBSERVABILITY_BIN`,
`DEVBRIDGE_UNIFIED_CANDIDATE_BIN` and `DEVBRIDGE_RETIRE_CANDIDATE_BIN`, each pointing
to the candidate binary directory. Approval must automatically start Implementation
without a second client Start action.
