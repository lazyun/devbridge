> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

# DATAROOT_BOOTSTRAP_SINGLE_WRITER_CLOSURE

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

Result: **DATAROOT_BOOTSTRAP_SINGLE_WRITER_CLOSURE_READY**.

This is the bounded implementation closure for the DataRoot bootstrap P1.
No new global architecture review or Global Final Acceptance was performed.

## Baseline and isolation

- Requested and actual starting branch: `dev_20260908_1913`.
- Requested and actual starting HEAD: `8e4b9d233104f2e3794604c1869aca55f10b500c`.
- Baseline difference: none; original working tree was clean.
- Implementation branch: `codex/dataroot-bootstrap-single-writer-closure`.
- Independent worktree: `/private/tmp/devbridge-dataroot-bootstrap`.

## Ownership boundary

The new startup sequence is:

```text
resolve/default path and canonicalize root (create absent root if needed)
→ read-only validation
→ acquire DataRoot/core-writer.lock (nonblocking OS lock)
→ revalidate under ownership
→ initialize/repair managed directories and atomically publish marker
→ open process logger and append bootstrap diagnostics
→ controlplane.OpenWithDataRoot (borrow the same live ownership)
→ acquire existing state/core-writer.lock and open/migrate SQLite
→ allocate runtime epoch and recover
→ serve
→ stop services and close SQLite
→ write final process diagnostic and close logger
→ release DataRoot ownership
```

Creating an absent root and its persistent lock artifact are the only writes
permitted before root ownership. Foreign directories, malformed/unsupported
markers and invalid existing managed paths are rejected by read-only preflight.
The owner rechecks the layout after acquiring the lock. A failed initializer
releases its handle and preserves retryable partial state. A lock-only root is
retryable after a crash. Lock filenames are never removed or used as a liveness
check; OS descriptor ownership remains authoritative. Root aliases resolve to
the same canonical path, and non-regular lock paths are rejected.

`dataroot.Open` now returns ownership requiring `Close`. Direct
`controlplane.Open` acquires it before any bootstrap writes and releases it on
failure or after service/DB shutdown. The Core entrypoint keeps its own handle
through final logging and passes it into `OpenWithDataRoot`; closed handles,
modified paths and mismatched configured roots are rejected. The borrowed plane
never releases the process's handle. Returning through `mainExitCode` ensures
failure exits run defers before `os.Exit`.

The existing SQLite descriptor lock and durable epoch fencing remain unchanged.
They protect direct SQLite callers; the outer root lock adds the earlier and
later Core lifecycle boundary. Marker format stays at version 1. A competing
Core exits with code 1 and the existing recognizable writer-conflict stderr
text before opening its DataRoot logger, changing layout, opening SQLite,
allocating an epoch, performing recovery or publishing readiness.

## Regression evidence

- Concurrent fresh `dataroot.Open`: exactly one live owner among 32 contenders.
- Concurrent fresh control planes: exactly one writer, valid canonical marker,
  and successful durable writes by the winner.
- Two fresh Core entrypoint subprocesses: exactly one readiness announcement;
  one initialization/start/ready/stop sequence in the shared log.
- Deterministic pre-marker ownership and recognized-root missing-directory
  cases: a competing real Core entrypoint cannot initialize or repair layout.
- Live Core contender: a recursive snapshot compares paths, modes, sizes,
  modification times and SHA-256 file contents, including marker, DB/WAL/SHM,
  lock artifacts and process log. The rejected process changes none of them.
- Normal shutdown and forced process death both permit reopening; stale lock
  artifacts do not block initialization.
- Initialization failure after acquiring the lock, SQLite-open failure and
  process startup failure release ownership for retry.
- Borrowed ownership survives plane shutdown, covering final logger cleanup.
- Root symlink aliases contend for one owner; closed/mismatched ownership and
  non-regular lock paths are rejected.
- Existing recovery/epoch regression proves a contender cannot recover or
  reassign the winner's live Run, claim or runtime authority.
- Desktop launches the built Core, recognizes its actual conflict stderr and
  verifies that the first Core remains ready.

## Validation

Commands use `GOCACHE=/private/tmp/devbridge-bootstrap-go-cache` to keep build
cache writes within the task's temporary area. Loopback/process tests were run
with the required local execution permissions. Desktop's existing Vite build
was generated in this worktree before its Go tests.

| Gate | Result |
|---|---|
| `go test ./... -count=1` | PASS; 54 packages with tests |
| `go test -race ./internal/dataroot ./internal/controlplane ./internal/storage/sqlite ./cmd/devbridge -count=1` | PASS |
| `go vet ./...` | PASS |
| `go build -trimpath -o <temporary-output>/ ./cmd/...` | PASS |
| Desktop writer-conflict real Core integration and classifier tests | PASS |
| Windows amd64 DataRoot test cross-compilation | PASS |
| Changed Go files `gofmt` and `git diff --check` | PASS |

Native execution evidence is macOS. Cross-compilation does not claim Windows
runtime validation. External real-Codex/ChatGPT E2E was not needed for this
bootstrap ownership change and is not claimed.
