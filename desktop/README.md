# Dev Bridge Desktop

Dev Bridge Desktop v0.1-B1 is a thin local operations client built with Wails
v2.10.2. It starts, stops, and observes local Dev Bridge services; it is not a
second control plane. Projects, Feature Areas, Tasks, Executions, Runs, Workspaces,
Git, Evidence, approvals, authorization, and durable domain state remain owned
by Dev Bridge Core.

The Desktop module is intentionally nested. It imports no Core internal package
and communicates only through local child-process and HTTP boundaries.

## Prerequisites and development

- Go 1.25 or later
- Node.js and npm
- the [Wails v2 prerequisites](https://wails.io/docs/gettingstarted/installation)
- Wails CLI v2.10.2, installed explicitly by the developer:
  `go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2`

The repository does not silently install global tooling. Prepare development
sidecars and run the shell with:

```sh
cd desktop
./scripts/build-sidecars.sh
cd frontend && npm ci && cd ..
wails dev
```

From the repository root, build a production Desktop application with:

```sh
./build-desktop.sh
```

This builds `devbridge`, `devbridge-mcp`, and `devbridge-local-mcp` under
`build/bin`, runs the configured Wails Desktop build, and stages the bundle as
`build/Dev Bridge Desktop.app` next to `build/bin`:

```text
build/bin/devbridge
build/bin/devbridge-mcp
build/bin/devbridge-local-mcp
build/Dev Bridge Desktop.app
```

The packaged application discovers these executables from the sibling `bin`
directory next to its bundle. Runtime resolution is derived from the running
Desktop executable location (never the working directory or a login shell
PATH), so a Finder launch works with an empty environment. The default bare
binary names in settings (`devbridge`, `devbridge-mcp`,
`devbridge-local-mcp`) participate in that discovery; an explicit absolute
path keeps override semantics, and a missing binary is reported as a
non-fatal diagnostic that surfaces the spawn-time error instead. Run Go and
frontend checks independently with:

```sh
cd desktop
go test ./...
go vet ./...
cd frontend && npm run check
```

For development inside the bundle, `wails dev` and raw `go run` layouts are
discovered through the repository root walk. Signing, notarization, auto-update,
and release delivery are deliberately out of scope.

## Runtime and ownership model

In **Settings → Coding Agent**, use **Read models from GPT** to read the
configured Codex App Server's model catalog. New models are added disabled;
enable a model and choose its default reasoning effort to use it. Existing
enablement and compatible defaults are preserved. Supported reasoning efforts
and Fast service tiers come from the catalog and persist across restarts.
Changes apply to a running managed Core without restarting it. A failed read
preserves the saved configuration. Codex may return a bundled or cached catalog;
the list does not verify account access to each model.

The startup sequence is strictly:

```text
Desktop -> probe/start Core -> wait for Core -> probe/start MCP -> wait for MCP -> READY
```

Defaults are configurable and remain `http://127.0.0.1:8081` for Core/Console
and `http://127.0.0.1:8080` for MCP. Endpoints must be loopback HTTP URLs with
an explicit port. Probes and shutdown waits are bounded.

- **MANAGED** means Desktop started the child and owns that exact process.
  Stop, Restart, and Desktop quit signal MCP before Core. A graceful timeout may
  force-terminate only these owned children.
- **EXTERNAL** means an already-running service was positively identified using
  its existing readiness/diagnostic protocol. Desktop attaches without a PID
  ownership claim and never stops it.
- **CONFLICT** means the endpoint responds but cannot be identified as the
  expected service. Desktop reports the endpoint and never kills its listener.

Core readiness authority is `/readyz` (HTTP 200 with `"ready":true`).
`/ui/` HTTP 200 verifies Console availability independently of presentation text.
`/_adapters/identity` provides runtime identity and authoritative DataRoot readback;
readback failure leaves Core ready with DataRoot omitted and a diagnostic detail.
MCP identification and build metadata use `/readyz` and the existing
`bridge_diagnostics` MCP tool. A managed Core build comes from its own
`DEVBRIDGE_READY ... build=...` output. The current Core HTTP protocol has no
standalone build field, so an EXTERNAL Core build displays `—`; Desktop never
substitutes checkout HEAD. A visible warning is shown when known Core and MCP
builds differ.

There is no automatic restart loop. An unexpected owned-child exit becomes
`CRASHED`, retains the exit detail and recent logs, and awaits manual Restart.

## Dev Bridge data directory

The single **Dev Bridge data directory** setting is the managed launch
`DataRoot`. One DataRoot is one independent local Dev Bridge environment:

```text
<DataRoot>/
  .devbridge-data-root.json
  state/
  worktrees/
  evidence/
  logs/
  runtime/
  artifacts/
```

The marker currently contains only `{"formatVersion":1}`. An absent or empty
directory initializes a fresh environment. A recognized existing directory
reopens that environment. A non-empty unrecognized directory, malformed marker,
newer format, file path, or unsafe managed child is rejected by Core before
normal startup.

Switching DataRoot never migrates, copies, merges, or deletes anything and does
not rewrite old worktree paths. The selection applies only to the next managed
Core/MCP startup, so Desktop requires managed services to stop before saving a
changed DataRoot. EXTERNAL services are not restarted or assumed to use the
Desktop selection.

Project repositories remain at their user-selected external paths. Only
Bridge-managed execution worktrees are materialized below
`<DataRoot>/worktrees/`. Core and MCP receive the same `-data-root` argument;
normal SQLite state and structured logs resolve to `<DataRoot>/state/devbridge.db`
and `<DataRoot>/logs/` respectively.

## Registering a Project

With Core ready, open **Projects** in Desktop and choose **Add Project**. The
native folder picker selects a local Git working tree. Desktop proposes the
repository directory name as the Project name; confirm or edit the name, then
create the Project. Core resolves subdirectories and symlinks to the canonical
Git root and durably creates only the Project and its Repository binding.

The new Project appears immediately in Desktop and is available to Local MCP
and GPT Adapter `project_list` / `project_open`. Registration does not run
Project Discovery, create Feature Areas or Modules, configure Codex, open an AI
session, or start work. Selecting a Project in an AI conversation remains a
separate explicit user decision.

## Console, Codex, settings, and logs

The Console page embeds the existing Core-served `/ui/` and provides **Open
Console** through the system browser when WebView origin behavior prevents
embedding. Console source and business logic are not copied into Desktop.

Codex availability is checked only with the configured executable and a bounded
`codex --version`. Desktop reports Available, Missing, or Error and never starts
a Codex execution for this check. When the default `codex` name is configured,
Desktop also checks the ChatGPT app bundle and common user-local/NVM install
locations so Finder launch does not depend on an interactive shell `PATH`. An
explicit executable path temporarily prepends its own directory to the probe
environment, allowing an NVM `#!/usr/bin/env node` launcher to find its sibling
Node executable.

Settings contain the selected DataRoot path, Core/MCP binary paths, the Codex
executable, and the two loopback endpoints. The native directory picker selects
folders only and leaves the previous value unchanged when canceled. Settings
are atomically persisted with owner-only file
permissions through the operating system's user configuration directory:

- macOS: `Library/Application Support/devbridge-desktop/settings.json` beneath
  the current user's home directory;
- Linux: `$XDG_CONFIG_HOME/devbridge-desktop/settings.json` (or the platform Go
  fallback);
- Windows: the current user's `AppData/Roaming/devbridge-desktop/settings.json`.

Desktop preferences remain outside DataRoot so the selected path is available
before Core starts. Desktop-owned local state is below the adjacent
`devbridge-desktop` directory; `logs/` is the reserved process-log location.
Desktop keeps only the newest 1,000
stdout/stderr lines in memory and does not yet persist them. Obvious
credential-shaped values are redacted, individual output is bounded, child
environment variables are never dumped, and no telemetry or upload exists.
These process logs are explicitly neither the authoritative Event Ledger nor
Evidence and must not be used to record full conversations or prompts.

## Deferred work

- **Desktop project management beyond registration**: rename, removal,
  deletion, multi-repository editing, and Project Discovery UI remain outside
  the thin Desktop shell.
- **Desktop v0.1-B — Execution Usage**: Input, cached-input, and output tokens
  require a stable Bridge read API. Unknown cached telemetry will display `—`,
  never zero.
- **Desktop v0.1-B — Managed Workspace Operations**: cleanup must flow through
  Bridge ownership/state validation. Desktop never performs arbitrary recursive
  deletion.

Other v0.1-A limits include no signing/notarization, updater, analytics, remote
fonts or telemetry.

## Desktop Console

The Console is a Desktop-owned task list and inspector, with a single navigation
rail. It reads Core's scoped project, task and run projections through fixed
Wails methods; it does not embed the web Console. The current task's phase,
verification gap, delivery state and eligible actions remain visible together.
Task filtering, paging, search, selection and run-result inspection stay inside
Desktop. Run history starts with the latest bounded page.

Continue, defer, stop execution and complete/merge use Core's exact action target
and current eligibility. Completion first reads Core's frozen merge plan. Each
operation has a local confirmation; its exact request body is durably retained
before sending. An unconfirmed result can only retry that same request, and
records for separate tasks are retained independently. Read failures preserve
the last display and disable commands until a successful refresh. Late reads
are fenced by the selected project, task and attached Core runtime.

Structured workflows without a native form have an explicit browser entry for
the exact project/task; they are never represented as having no pending action.
Core still owns admission, permission checks, required verification and merge
validation. The Run sessions tab retains its existing focused stream layout.

## Pi model picker

Settings → Pi loads a read-only model catalog. Search the dropdown by model
name or provider, select one result, then Add model to persist that model.
Existing entries are marked Added and keep their enablement and defaults.
Loading or a failed catalog read never replaces configured models.

Pi configuration follows two steps: select models to use, then choose a default
from the enabled selection. Disabling the default clears that preference; it
does not silently select another model. Defaults captured by a managed Pi
environment require stopping the services before editing.
