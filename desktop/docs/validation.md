# Desktop v0.1-B1 validation evidence

Validated on 2026-09-05 from Core base
`cab6b206c37ab2cc042116f39d5ef999729c3810` with Desktop foundation
`7431865ef845038036552ec8dcca96188fc03895` integrated cleanly.

## DataRoot regressions

- absent and empty roots initialize marker format 1 plus `state`, `worktrees`,
  `evidence`, `logs`, `runtime`, and `artifacts`
- valid roots reopen idempotently
- non-empty arbitrary roots, malformed markers, unsupported versions, files,
  and symlinked managed children are rejected
- failed initialization leaves no valid marker
- A → fresh B → A restores A's durable Project and Feature Area without sharing logs,
  Evidence, artifacts, SQLite, or worktree roots
- a real Git worktree is created below `<DataRoot>/worktrees/`; a source
  repository remains external and symlink-root replacement is rejected

The bounded real local probe started Core and MCP against Root A, created a
Project, stopped both, started both against fresh Root B, then reopened Root A.
Observed Project counts were `1 → 0 → 1`; the original Project ID returned.
Both roots contained marker format 1, `state/devbridge.db`, and Core/MCP JSONL
logs below `logs/`. A non-empty invalid directory was rejected with the stable
message and its only file remained unchanged.

## Root module

- `go mod verify`: pass
- `go test ./...`: pass
- `go test -race ./...`: pass
- `go vet ./...`: pass
- focused DataRoot/control-plane/worktree/observability/smoke tests with
  `-count=1`: pass
- `bash -n scripts/devbridge-smoke.sh`: pass

## Desktop module

- `go mod verify`: pass
- `go test ./...`: pass
- `go test -race ./...`: pass
- `go vet ./...`: pass
- frontend `npm run check` (Vite 6.1.0 production build): pass
- Wails CLI v2.10.2 production application package: pass

The root `go.mod` and `go.sum` remain unchanged and contain no Wails/Desktop
dependencies. Generated binaries, frontend dependencies/build output, probe
roots, validation logs, and runtime processes are removed after validation.
