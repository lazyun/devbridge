# Dev Bridge 开发里程碑（历史归档）

> 本文保留原 README 中的开发记录，包含已被后续实现替代的设计、命令和限制。
> 当前使用方式请从[项目首页](../README.md)和[用户指南](user-guide/README.md)开始；
> 精确接口以[接口参考](interfaces.md)及对应架构合同为准。

# Dev Bridge

Task is the durable engineering intent and the sole semantic phase authority.

```text
Project → FeatureArea → Task
                       ├─ Review [0:N] → Run [1:1]
                       └─ Execution [0:N]
                            ├─ Implementation Run [0..1]
                            └─ Verification Run [0..1]
```

FeatureArea owns the canonical Workspace and AgentConversation. Its lifecycle is
DISCOVERING, ACTIVE, RETIRING, RETIRED or DECOMPOSED. Tasks own neither resource.
One active agent operation per FeatureArea is enforced by the existing durable
claim; independent FeatureAreas can run concurrently. Project-scope Inspection
keeps its internal operation representation and is not a FeatureArea Task.

The [Task-owned v46 architecture](architecture/task-execution-v0.1.md) defines
the Task-owned contract. A Task records intent before Review; current Review
Approval freezes its initial revision. An explicit start selects the implementation
runtime. GPT uses purpose-specific preparation and confirmation components; Local
MCP retains direct commands such as `task_create` and `execution_start`. Only Task
Acceptance can accept the completed delivery.

## User documentation

Start with the [Dev Bridge User Guide](user-guide/README.md) for the
user-facing product model, getting-started flow, workflow, roles, decisions,
and stable v0.1 behavior. The milestone notes below describe implementation
history and internal details. Pre-v44 workflow statements there are historical,
not supported compatibility paths; use the linked v46 contract.

To exercise the current packaged workflow in disposable local repositories, run
`./scripts/verify-usable-project.sh`. It builds Core and both adapters and checks
the full public workflow through Git delivery and process restart using an
offline deterministic App Server. See [workflow acceptance](testing/real-e2e-gates.md)
for its coverage and evidence paths.

M0 is a minimal Go client for the local Codex App Server protocol. It starts
`codex app-server` directly, completes the initialize handshake, starts a
thread and turn, correlates streamed lifecycle events, captures the final agent
message, and shuts the child process down.

## Development Branch selection

Projects now have an explicit, durable Development Branch selection per registered
Repository, owned by the current DevelopmentCycle. Project selection keeps
FeatureArea unselected. Both adapters can read `workspace_get(include=["BRANCHES"])`.
GPT uses `project_setup_prepare` with a confirmation component; Local MCP uses
`development_branch_select`. The Console provides a Development Branch selector
beside the Project selector. Selection only updates
Bridge metadata and never runs Git checkout. Reselection preserves history, and
ref movement/deletion is reported without fallback.

See the [implemented architecture boundary](architecture/development-cycle-work-model.md#implemented-boundary-development-branch-selection-schema-v33)
and [interface reference](interfaces.md#development-branch-context).
FeatureAreaCycleBinding and the remaining Git/release lifecycle are deferred.

## M1 execution control plane

M1A-M1E add a local, in-memory execution control plane above the frozen M0
Codex gateway:

```text
Project -> FeatureArea -> Task -> Execution -> Run
FeatureArea -> AgentConversation -> canonical Codex Thread
FeatureArea -> canonical Workspace -> Worktree
Run -> workspace association + Codex Turn + Git Evidence
```

- `Repository` registers a canonical path to an existing local Git repository
  under a Bridge-owned ID. Registration validates the repository through the
  centralized Git client and deterministically reuses a prior registration of
  the same canonical path.
- `Task` captures an explicit `WorkSpec`, `VerificationSpec`, and immutable full
  base commit. A Task describes a confirmed execution plan; implementation and
  optional verification are separate purpose-tagged Runs.
- `Run` owns strict preparation, execution, evidence-collection, and terminal
  states. Per-Task attempt numbers are allocated atomically by the in-memory
  store.
- `Worktree` is an isolated Bridge-owned execution directory. M4 paths use
  `<configured-root>/<workspace-id>` and branches use
  `devbridge/<feature-area-id>/<workspace-id>`. Source working-tree dirt is never
  copied. The first workspace starts at the selected Development Branch commit;
  later Tasks reuse its commits and uncommitted work.
- The `Execution Coordinator` creates the worktree before starting Codex and
  supplies only that worktree as `thread/start.cwd`. It never falls back to the
  source repository. Caller cancellation is propagated through Codex and
  recorded as a canceled Run while the worktree is retained.
- `Git Evidence` records cumulative Task-base truth plus immutable start/end
  Git tree snapshots for each Run. Per-Run changed files and diff are computed
  between those trees, including untracked additions and deletions, without
  changing the real index. The final Codex message remains a separate
  `AgentClaim`, not proof.

At the M1 checkpoint, stores are process-local and race-safe. M2 retains those
stores for unit tests and adds the durable production composition described
below. The full M1 real-Codex E2E campaign and M1 Gate remain intentionally
deferred.

The production code under `internal/codex` remains the M0 boundary. Its only
post-M0 extension is typed, transport-owned token telemetry described below;
the diagnostic event stream remains outside lifecycle correctness.

## M2 durable control plane

M2 makes machine/runtime state durable without adding an API or an AI-facing
communication layer:

```text
Project -> Repository -> Task -> AgentExecution -> Run -> Worktree / Evidence / Artifacts / Approvals
                    SQLite current state + append-only Event Ledger
                                      |
                               startup recovery
                                      |
                         M1 Execution Coordinator
                                      |
                          M0 CodexGateway
```

- A `Project` is a Bridge-owned logical boundary that may own multiple
  repositories. Each repository belongs to exactly one Project, and its
  canonical path is globally unique in a Bridge instance. A Task records its
  Project and currently targets exactly one repository.
- SQLite is authoritative for Projects, Repositories, Tasks, Runs, Approvals, Worktree
  metadata, structured Git Evidence, Artifact metadata, and Events. M2 uses
  the pure-Go `modernc.org/sqlite` driver, ordered transactional schema
  migrations, UTC `RFC3339Nano` timestamps, foreign keys, a bounded busy
  timeout, and WAL mode.
- The Event Ledger uses a SQLite-assigned monotonic sequence and versioned JSON
  payloads. It is append-only operational history, not an event-sourced
  replacement for the current-state tables. State transitions and their event
  append occur in the same short transaction; Git, Codex, and filesystem work
  run outside database transactions.
- Run attempts are allocated by atomically incrementing the parent Task's
  persisted counter with `UPDATE ... RETURNING`. The `(task_id, attempt)`
  unique constraint remains the database backstop across concurrent store
  instances.
- Artifact bytes live under
  `<state-root>/artifacts/<run-id>/<artifact-id>`. Files use generated IDs,
  are written and synced as temporary files, then atomically renamed before
  metadata is inserted. Metadata records the actual byte count and SHA-256.
  If metadata insertion fails after finalization, the unreferenced file is an
  accepted M2 orphan; artifact garbage collection is deferred.
- The default state root is `~/.devbridge`, containing `devbridge.db`,
  `artifacts/`, and `worktrees/`. Tests and callers can configure another root.
- Startup opens SQLite, completes migrations, constructs durable stores, and
  runs recovery before the control plane becomes ready. A Run left in
  `PREPARING`, `RUNNING`, `WAITING_APPROVAL`, or `COLLECTING` becomes
  `INFRASTRUCTURE_FAILED` with code `bridge_restart_interrupted`; its parent
  Task is reconciled from `RUNNING` to `FAILED`. `CREATED` and terminal Runs
  are unchanged, and recovery is idempotent.
- Worktrees are retained for inspection. Recovery records a missing persisted
  worktree as `MISSING` and appends a recovery event; it neither recreates nor
  deletes the path or branch.

M2 does not implement MCP, ChatGPT integration, Memory/Decision knowledge,
scheduling, review/acceptance, merge automation, remote workers, or distributed
coordination. The full real-Codex E2E campaign and the M2 Gate are deferred.

## Post-M2.5 domain alignment

The durable execution domain is now:

```text
Project
├── Repository *
├── Module *
│   └── Task *
│       ├── fixed CodexConfigID
│       └── Run *
└── CodexConfig registry (Bridge-managed execution choices)
```

- A Module is Bridge registry data and a semantic classification boundary,
  not a repository child or a free-form label supplied by an AI. Module keys
  are stable and unique within a Project; optional parents must remain within
  the same Project and cannot form an obvious cycle.
- Every newly created Task belongs to exactly one Module and still targets one
  Repository. Both the Module and Repository must belong to the Task's
  Project.
- Every Task stores one immutable `CodexConfigID`. Switching configurations
  requires a new Task. Config definitions may change without revisions or
  historical snapshots.
- A Codex config stores the concrete Codex launcher path used by the current
  gateway's existing `CodexPath` mechanism. An empty path inherits the
  control-plane/default `codex` launcher. The registry does not introduce a
  second launch mechanism into M0.
- A Run durably records `CreatedAt`, `StartedAt`, and `FinishedAt`.
  `StartedAt` is set once when the Runtime Supervisor takes actual `RUNNING`
  ownership, not during preparation. `FinishedAt` is set once for every
  terminal outcome. `WAITING_APPROVAL` is a distinct nonterminal projection
  within the same execution lifetime; `COLLECTING` is also nonterminal.
- Per-Run token accounting starts as `PENDING`. The typed
  `thread/tokenUsage/updated` notification supplies cumulative input, cached
  input, cache-write input, output, reasoning-output, and total counters, plus
  a non-consumption model context-window value. A reliable Run total is the
  end cumulative snapshot minus its start snapshot; repeated cumulative
  notifications replace the end snapshot and are never summed. Missing,
  malformed, decreasing, or unattributable telemetry finalizes as
  `UNAVAILABLE`, never as an estimate.
- Task, Module, and Project token totals are derived from `AVAILABLE` Runs;
  no duplicate aggregate counters are persisted.

Dev Bridge still performs no NLP or free-form intent inference.

## M2.8 asynchronous runtime ownership

The `RuntimeSupervisor` owns managed Run lifetimes after a successful start
handoff, so cancellation of the requesting control API call does not cancel an
accepted Run. Different Tasks may execute concurrently, while an in-memory
task lease and the v6 SQLite partial unique index jointly enforce at most one
`PREPARING`, `RUNNING`, `WAITING_APPROVAL`, or `COLLECTING` Run per Task.
`WAITING_APPROVAL` retains the same task ownership.

Cancellations with a durably correlated Codex turn retain the Workspace for an
explicit later continuation; failures before turn correlation or failures
that make the protocol lineage ambiguous mark continuity unavailable.
Shutdown stops new handoffs, interrupts
in-progress handoffs and live sessions with `bridge_shutdown_interrupted`, and
joins every owned session before closing SQLite.

## Human approval lifecycle

Managed execution selects M0's narrow typed interactive approval mode. Each
Codex approval request is first persisted as `PENDING` with its correlation,
request payload, and legal decisions; the Run projects to
`WAITING_APPROVAL`. A human decision is committed separately as `DECIDED`,
then delivered to the live Codex transport, and only a confirmed protocol
write is committed as `DELIVERED`. The Run returns to `RUNNING` only when no
`PENDING` or `DECIDED` approvals remain. The Task remains `RUNNING`, the Run
lease remains owned, `FinishedAt` remains unset, and token accounting remains
`PENDING` throughout the wait.

`DECLINE` denies only the requested action and permits the turn to continue.
`CANCEL` terminates the Run as `CANCELED`. Permission grants copy exactly the
requested permission object and cannot broaden it. Delivery failures leave the
human decision non-delivered and fail the Run closed. On restart, open
`PENDING` and `DECIDED` approvals become `EXPIRED` (preserving a recorded
decision), delivered approvals remain delivered, and the interrupted waiting
Run follows the existing infrastructure-failure recovery path.

## M3-A MCP transport foundation

M3-A adds an AI-facing MCP adapter without wiring task creation or execution.
It uses the official Go MCP SDK's stateless Streamable HTTP transport and
returns deterministic `application/json` responses for ordinary discovery and
RPC calls. The only tool at that milestone was the read-only, non-destructive
diagnostic bootstrap tool. The adapter depends on a narrow injected service
interface; domain and control-plane packages do not import MCP packages.

Run Core and point the GPT adapter proxy at it:

```bash
go run ./cmd/devbridge -mode server -data-root /tmp/devbridge-data -listen 127.0.0.1:8081
go run ./cmd/devbridge-mcp -data-root /tmp/devbridge-data -core-url http://127.0.0.1:8081 -listen 127.0.0.1:8080
```

Core is the only DataRoot writer. The adapter verifies Core identity and
proxies model-facing requests to Core's private GPT handler.

The MCP endpoint is `http://127.0.0.1:8080/mcp`. Liveness and readiness are
available separately at `/healthz` and `/readyz`. Non-loopback listeners are
rejected unless explicitly enabled with `-allow-non-loopback`; authentication
for non-local deployments is outside this transport-only slice.

The real external transport gate passed through ChatGPT, the OpenAI Secure MCP
Tunnel, the local tunnel client, and Dev Bridge MCP. The observed diagnostic
result reported Dev Bridge version `m3-a`, server `devbridge` / `m3-a`,
`ready: true`, protocol `MCP`, MCP protocol version `2026-07-28`, and transport
`streamable-http`.

## Explicit Project selection and engineering intent

For connectivity-only requests, the diagnostic capability reports status and
may finish the request. A request to start using Dev Bridge continues after
readiness through `project_list`, candidate presentation and a stop, a subsequent
explicit user choice, then `project_open(projectId)` returning the reusable
`aiSessionId`. `project_selection_show` is the independent initial picker.
The initial start-use request never selects a Project or creates a Task. The
durable selection is single-use and is consumed
atomically with creation of the immutable Project-bound AISession. Console view
selection, the only available Project, previous Projects, and admin-fixture
sessions do not authorize this AI-facing flow. Module, Task, Repository, and
Codex-config reads are scoped through the resulting binding.

`task_create` saves semantic development intent in a usable FeatureArea.
`task_review_start` separately starts its read-only Review. After reading the exact
Review result and current decision, `decision_respond(kind="REVIEW_APPROVAL")`
records the user’s APPROVE / REJECT / REVISE with a stable requestId. APPROVE
freezes the initial definition on the same Task and enters IMPLEMENTATION_READY.
`execution_start` is the separate start command.

## FeatureArea Review and approved development

## Legacy M3-B3 confirmed asynchronous execution

`ExecutionProposal` is the durable, explicit second human gate: it links one
applied TaskPlan to its Task and can move only from
`PENDING_CONFIRMATION` to `CONFIRMED` to `CONSUMED`, or from pending to
`REJECTED`. Project and Task authority are derived from the immutable AI
session and exact TaskPlan; the AI-facing API accepts no Project ID and
cannot start an arbitrary Task. SQLite migration v7 adds this audit link and
records the resulting Run ID.

Starting a confirmed proposal atomically consumes it with creation of exactly
one durable Run attempt, then hands that exact Run to `RuntimeSupervisor`.
The call returns after the asynchronous handoff. Handoff failure never reuses
the authorization for a hidden attempt. Continuations keep the Task's Module,
Codex configuration, base commit, and original goal intact while delivering
the confirmed continuation instruction to the new Codex turn.

The control API no longer exposes split TaskPlan/Execution confirmation, apply,
or start routes; every user decision writes through the authoritative contract
(`ConfirmTask`, `RejectTaskPlan`, `AuthorizeAndStartExecution`,
`RejectExecutionProposal`) reached only via the app-private MCP Decision tools.
Production MCP exposes the current product operations and app-private Decision
tools. Legacy `execution_proposal_*`, `run_get`, `run_list`, `run_cancel`, and
approval registration helpers are not the production discovery surface. Active
Execution cancellation uses the shared `execution_cancel` operation with exact
Execution/Run/request identity; the legacy control API rejects workflow-owned
Runs. There is deliberately no MCP `run_start`; confirmed execution proposals
are the only AI-facing start path.
Run and approval reads return bounded structured facts, and all new operations
validate the Run/Approval Task's Project against the AI session before reading
or mutating it. Runtime approvals retain their separate durable decision and
delivery states.

The checked-in protocol schema was generated with Codex CLI `0.149.0`:

```bash
codex app-server generate-json-schema --out api/codex-schema
```

## Workspace and AgentConversation ownership

FeatureArea owns one canonical current Workspace and one independent canonical
AgentConversation. Terminal Tasks leave the area ACTIVE / FEATURE_AREA_IDLE, with
no current active Task, the latest terminal Task retained, and CREATE_TASK available.
See [continuous FeatureArea work](architecture/feature-area-continuation.md).

Reset rotates an idle FeatureArea conversation explicitly and retains its workspace. UNKNOWN runtime work
requires exact agent_operation_recover. Neither workspace recovery nor Task
creation chooses an old Thread or invents a replacement context. Legacy
Workspace Thread values remain audit history only.

## M5-A Project context and classification

SQLite migration v9 adds durable `ProjectContext` and GPT-authored
`ContextProbe` acquisition. SQLite migration v16 extends that state with a
cheap structural fingerprint and a durable production-backed Project Module
Map. Project open now reads and preserves root `AGENTS.md` when present and
performs only bounded production-structure inspection; without root guidance it
builds the same map through lightweight production discovery and records usable
`NO_GUIDANCE`. It does not inspect tests or run test, build, lint, static-analysis,
or verification commands. A replacement AI session reuses a valid fingerprinted
map; ordinary source edits do not invalidate it, while guidance, Repository
identity, production boundaries, relevant manifests, or invalid mapped roots do.
A scoped
probe runs as an independent read-only Codex execution with its GPT-authored
instruction unchanged inside a fixed envelope. Its timing, thread/turn IDs,
result, failure, and token usage remain durable on the INSPECTION Task → Execution → Run chain.

One Project owns many immutable Project-bound AI sessions and exactly one
system-owned `_unclassified` Module. Tasks remain Project-owned and
non-null-classified; GPT may initially select `_unclassified`, then move an
inactive Task once to a regular Module. The transition records AI-session
provenance, while every AI session bound to the Project can still see the Task.
Bridge performs no NLP or semantic classification, and interrupted probes are
reconciled to `FAILED` without retry or scheduling.

## M5.1.5-C Review product boundary

Project Context answers how the selected Project is organized; Feature Area Review
answers what production implementation is necessary for one engineering goal.
The model-facing Project Module Map uses stable keys, names, responsibility
summaries, Repository names, exact Repository-relative production roots,
entrypoints, and bounded relationships—never internal Module or context IDs.

SQLite migration v17 introduces a durable `review_<opaque-id>` product object;
v18 records the single durable semantic-continuation attempt for a terminal
Review.
Each Review belongs to one Project and Feature Area and is distinct from its linked
internal `SCOPE_REVIEW` Run. A Feature Area may have any number of intentional Reviews,
including multiple successful Reviews with the same focus or prompt.

`task_review_start` requires a stable `requestId` and accepts either a strict `FOCUSED` plan with `PRIMARY` and
`SUPPORTING` module keys or explicit `REPOSITORY` mode. The resolved roots are
persisted as Review focus: they tell Codex where to start and enable aggressive
pruning, but they are not a read-authorization boundary. During the Review,
Bridge's trusted Codex Thread/Turn mapping selects the authorized Project and
Repository. Codex may use normal backend engineering inspection and follow a
materially relevant dependency outside the initial focus without a ScopeExpansion
decision. Bridge structurally blocks traversal/symlink escapes and cross-Repository
access, but does not classify command text, test-like paths, or repository inspection
strategy and does not police Review worktree mutation as a semantic role contract.
The current v0.1 single-Repository Run model does not attempt ambiguous
cross-Repository Review.

Review success requires successful backend completion and a nonempty, bounded
engineering result. It does not require `repository_*` capability evidence; native
backend inspection is trusted within the authorized Repository. Review admission
atomically records the request, Review, Run, claim, grant, and immutable repository
provenance. That provenance distinguishes the frozen Review baseline from
admission-time Repository HEAD, full branch ref or detached state, and
source-worktree dirty state. Review inspection itself uses a managed worktree
created from the frozen commit, so uncommitted source contents are not implied to
have been reviewed.

`operation_get(aiSessionId, target, include=["RESULT", "EVIDENCE"])` exposes the bounded product result and its
request/Review/Run correlation without Workspace/Thread/Turn lineage. Its observed
inspection counters cover successful Bridge Host Tool events only; zero does not
mean that the trusted backend failed to inspect by another allowed read-only
mechanism. Authorization, AgentClaim, observed inspection, and result availability
remain separate facts. The exact engineering-only Codex prompt is stored with
its SHA-256, byte length, and sent timestamp for a local Console View/Copy flow;
operational logs contain only its fingerprint.

## M5.1.8 trusted Agent runtime, prompt passthrough, and Documents

Authored Review, implementation, and verification instructions retain their exact
bytes through the trusted Agent runtime. Durable Project knowledge is written
only when explicitly requested, through `document_create`, `document_update`,
and `document_link`. Read it with `document_get` or `document_list`; Documents
remain separate from Review proposals, Run evidence, and Task decisions.

## M5-B Work and verification execution planning

SQLite migration v10 makes every new Task definition explicit about both what
Codex may change and how the result is verified. `WorkSpec` stores the goal,
constraints, an explicit `PATHS` or `REPOSITORY` execution scope, and a
GPT-authored implementation instruction. `VerificationSpec` is either `RUN`,
with scope, structured checks,
acceptance criteria, and its own GPT-authored instruction, or `NONE`, with a
durable `USER_WAIVED` reason. Pre-v10 rows receive a clearly labeled
`LEGACY_UNSPECIFIED` migration waiver; new TaskPlans cannot create one or
execute through a legacy orchestration path.

An `ExecutionProposal` freezes both specs and authorizes one serial plan. A
successful `IMPLEMENTATION` Run is followed by one `VERIFICATION` Run only when
the confirmed mode is `RUN`. Both Runs use the same FeatureArea Workspace, worktree,
and Codex thread but distinct turns, snapshots, timing, evidence, and cumulative
token deltas. Implementation failure prevents verification; verification
failure prevents overall success; no repair or retry Run is created. A `NONE`
plan creates no fake verification Run and reports verification cost as
`NOT_REQUESTED` in the MCP read model.

Bridge sends each stored GPT instruction verbatim, with routing and product facts
kept out of the natural-language prompt. For implementation, Bridge canonicalizes `PATHS` at
proposal creation and validates each implementation Run's immutable
`ChangedFiles` delta against the scope. `OUT_OF_SCOPE_CHANGE` retains Git
Evidence and the dirty worktree, performs no automatic revert, and prevents
Verification from starting. VerificationScope remains independent.
Verification may return an unrestricted natural-language AgentClaim and must
leave its start Git tree unchanged. Deterministic command checks are evaluated
only from bounded Codex App Server command-execution items (the exact declared
command matched to a protocol-provided command action, raw wrapper, exit code,
output, duration, and lifecycle timestamps), and
typed Git assertions are evaluated only from collected Git Evidence. Missing
evidence fails with a precise domain code; prose never proves a check passed.
CUSTOM checks remain semantic and are reported only as an available
AgentClaim, never deterministic PASS. Mutation terminalizes the Run with
`VERIFICATION_MUTATED_WORKTREE`. On first thread creation Bridge calls pinned
Codex 0.149.0 `thread/name/set` with the exact Task ID. Naming is display-only:
failure records `codex.thread_name_failed` and never fails execution.

SQLite migration v11 adds Task-first PLANNING, Project-owned TaskPlans,
fixed-string `repository_search`, and `SCOPE_REVIEW` Runs. Every semantic
instruction is GPT-authored, persisted, displayed, and sent byte-for-byte without
a Bridge metadata or role envelope.

## M5.1.2 evidence verification and interactive decisions

SQLite migration v13 adds only the durable `ScopeExpansion` authorization
needed where an exact read/review scope must be widened. The current schema records exact Review, FeatureArea, repository and
AI-session provenance, the current scope, exact additional repository-relative
scope, reason, and the one-way `PENDING_CONFIRMATION -> ALLOWED|REJECTED`
decision. It is domain authority—not UI state—and never starts Codex or changes
the Task goal by itself.

The MCP adapter publishes one deterministic embedded MCP Apps resource at
`ui://devbridge/decision-card` with MIME type
`text/html;profile=mcp-app`. Product Task, Execution, Approval,
ScopeExpansion, review-progress, and Result projections use the same
self-contained responsive UI; pure model-facing read/list/get/search tools do
not mount it. It uses the MCP Apps `ui/initialize`, tool-input,
tool-result, and standard `tools/call` message flow. Deterministic tool results
replace the projection in that same mounted app. Semantic replanning and an
explicit result-summary action use the MCP Apps 2026-01-26 `ui/message`
request, gated by `hostCapabilities.message`, with a bounded user-role text
block. The app has no CDN, remote fonts, analytics, or durable card state.

Legacy product Tasks expose Confirm/Cancel; one Confirm click performs the
durable internal confirm/apply continuation, leaves the executable Task ready
with zero new Runs, creates the exact pending Execution, and immediately
renders its Execute/Cancel decision. The mechanical internal primitives are
not model-visible compatibility aliases.
The decision app keeps active cards compact: bounded goal, grouped scope, key
constraints, and verification summaries remain visible, while exact files,
full constraints/checks, Evidence or AgentClaim details, IDs, and raw enums
share one collapsed Details section. Its common action renderer always places
negative/no-change actions first and left, and positive authorization actions
last and right; narrow layouts preserve that order when stacking.
Legacy pending Executions expose Execute/Cancel; one Execute click confirms,
single-use consumes, reserves, and asynchronously hands off the implementation
Run. The mounted app polls only the bounded app-private
`ui_workflow_state_get` projection while visible; model-facing `operation_get`
provides bounded Result/Evidence inspection without mounting new cards. The app
resumes progress rendering after the existing
`PENDING -> DECIDED -> DELIVERED` path. ScopeExpansion buttons
first record the exact durable decision and only then request semantic
replanning with `ui/message`. Terminal Execution state is independently rendered;
`Ask GPT to summarize` is an explicit semantic action and directs GPT to read
authoritative Evidence separately from AgentClaim. A terminal Feature Area Review sends
one bounded semantic continuation only after durable terminal state exists. The
attempt is claimed durably by Review identity, so reconnect, remount, restart,
or repeated app polling cannot emit it again. Successful continuation reports
that findings can support Task formation without authorizing implementation;
failed continuation keeps the Feature Area available for appropriate recovery.
Stale/repeated actions are
rejected by the underlying durable state transitions. Product-card decisions
remain distinct from MCP-host permission prompts.

## M5.x Execution goals and context continuity

Historical `task_create` accepted optional ordered Goals alongside the required input.
Current production `task_create` saves intent before `task_review_start`;
the following Goals behavior describes persisted legacy Tasks.
`goals` is an ordered list of 1–16 GPT-authored Task Goals, each a plain title of
at most 512 bytes. Goals are set once per Task, stored in stable position order,
and never mutate. When the Task's first Execution initializes, Bridge materializes
one PENDING progress row per Goal and the Implementation Run receives the
app-private `report_goal_progress` dynamic tool so Codex reports each goal's
observed status. Transitions are closed: `PENDING`/`RUNNING` may move to
`RUNNING`, `COMPLETED`, `FAILED`, or `SKIPPED` only forward, terminal statuses
never change, unknown goals are rejected, and a same-status report is an
idempotent no-op. Legacy detailed Execution projections retain ordered Goal Progress. Consolidated reads keep full Task and operation details opt-in.

Explicit Reset Agent Context uses `agent_context_reset` with
`aiSessionId`, an existing `featureAreaId`, and an idempotent
`requestId`. It preserves the old conversation and operation provenance, creates
a current successor, and makes the next operation start a new Thread. Active
Agent work returns `FEATURE_AREA_BUSY`. Unknown context boundaries fail closed;
no time expiry, token threshold, automatic compaction reset, context summary, or
Thread fork is introduced. Project Discovery retains separate Project context.
See [the architecture model](architecture/development-cycle-work-model.md#22-featurearea-agentconversation)
for boundary, recovery, and lifecycle semantics.

## Local Bridge Console v1

`devbridge -mode server` embeds a desktop-first local web console at `/ui/`.
It is a status and control surface—not an AI conversation UI—and uses bounded
HTTP projections backed by typed control/domain services. The console shows
Projects, ProjectContext and ContextProbe state, repository HEADs, Modules,
immutable AI-session Project bindings, Tasks grouped by Module, concurrent
active Runs, approvals, worktree/thread continuity, timing, token accounting,
and bounded Git evidence. Git evidence is presented as authoritative while an
AgentClaim is explicitly labeled as an agent report only.

Console Project selection is UI view state only. It does not affect future AI
sessions, rebind existing sessions, launch Codex, or scan a repository. Web Review Approval uses the same Core Decision as GPT, Local MCP and Card. Runtime
approvals remain read-only in the console; ChatGPT remains the primary approval
interaction.

The UI, CSS, and JavaScript are compiled into the Go binary. It has no CDN,
remote font, or Node build dependency. It inherits the server's explicit
loopback-only listener requirement and applies a same-origin Content Security
Policy. Repository source paths and Codex launcher paths are excluded from
console JSON; worktree paths are shown only as existing bounded execution
metadata. Approval payloads are bounded and redact credential-shaped fields.

Start it with a disposable state root:

```bash
go run ./cmd/devbridge \
  -mode server \
  -state-root /tmp/devbridge-console-state \
  -listen 127.0.0.1:8081
```

Then open `http://127.0.0.1:8081/ui/`.

## Smoke tests

For the production Bridge process and real Codex E2E gate, including the
loopback-only test administration surface, see
[`docs/testing/real-e2e-gates.md`](testing/real-e2e-gates.md).

The response smoke test uses a temporary working directory by default:

```bash
go run ./cmd/devbridge -mode response
```

Pass `-cwd` only for a directory that is safe for Codex to inspect. The
filesystem mutation smoke test always creates and later removes its own
isolated temporary directory:

```bash
go run ./cmd/devbridge -mode mutation
go run ./cmd/devbridge -mode all
```

M0's Bridge-owned server-request policy explicitly declines supported approval
requests and returns method-not-found for unsupported requests. Permission
upgrade requests receive an empty permission grant, which grants no additional
access. The policy never silently approves a request. Managed execution must
explicitly select the bounded, transport-owned interactive mode and consume
`NextApproval` / `ResolveApproval`; no arbitrary callback participates in the
transport lifecycle.

App Server stderr is always drained into a bounded 64 KiB internal capture.
Callers can inspect a concurrency-safe snapshot through `Client.StderrSnapshot`;
arbitrary external writers do not participate in process lifecycle ownership.

`Client.Events()` is a bounded, best-effort, diagnostic-only stream. Multiple
consumers compete for events; delivery is not broadcast. Events may be dropped
when the bounded buffer is saturated, and the corresponding drop diagnostic is
also best-effort. RPC and turn lifecycle correctness is handled internally and
does not depend on consumers reading this public stream.

Lifecycle-critical token accounting does not consume `Client.Events()`.
The client decodes the exact typed token notification inside its owned
notification path, correlates it to the active thread and turn, and returns a
bounded snapshot with the completed turn. This adds no caller callback or
additional stdout reader and cannot block the transport read loop.

Server requests run through four fixed workers and a bounded 32-request queue.
Responses use a separate bounded 64-response writer queue, so the stdout read
loop never writes to App Server stdin. Response-queue saturation terminates the
transport instead of blocking or silently dropping a mandatory reply. During
shutdown, queued replies receive a short best-effort flush window; the
transport then closes unconditionally so a peer that stopped reading cannot
block owned-process cleanup.

At most one `turn/start` may be pending for a thread. Before its response
identifies the authoritative turn, Dev Bridge retains at most 64 same-thread
lifecycle candidates; exceeding that bound fails the call explicitly. Retired
turn IDs are tracked in a bounded 256-entry set, preventing ordinary late
lifecycle notifications from immediately recreating canceled, abandoned, or
completed state. These are not permanent tombstones: after an ID is evicted, an
extremely late notification can recreate one idle remembered state. Ordinary
turn-state compaction still limits remembered state to 256 entries.

## Verification

```bash
go test ./...
go test -race ./...
go vet ./...
```

The generated schema in `api/codex-schema` is the protocol authority for the
wire structs under `internal/codex/wire`; M0 intentionally does not include a
general schema-to-Go generator.

## M5.1.2.6 external workflow model

The historical pre-v38 façade used Task → Execution → Result. Current development uses:

```text
Requirement → FeatureArea → Review → Approval → DEVELOPMENT Task → ExecutionProposal → Execution → Runs → Result
```

This is a façade over proven durable internals, not a broad internal rename:

| Product term | Internal audit model |
|---|---|
| Feature Area | durable capability directly owning Reviews and explicit Tasks |
| Review | durable Feature Area child linked to an internal trusted-backend Review Run |
| Task | durable Upper LLM work definition |
| Execution | universal durable `agent_executions` envelope for all four work kinds |
| Result | terminal Execution projection |
| Run | exact Execution-owned backend attempt and evidence checkpoint |

Review execution maps to the internal `SCOPE_REVIEW` foundation. Workspace,
Worktree, Codex Thread, Run evidence, token accounting, approvals, and execution
safety remain fine-grained internal mechanisms. A Feature Area can have multiple Task
lineages over time, although multi-Task orchestration remains deferred.

Schema v14 creates Feature Areas, links internal Tasks to them, and deterministically
backfills historical Tasks with `topic_backfill_<task-id>`. It preserves existing
TaskPlan, ExecutionProposal, and Run provenance.

The GPT model catalog contains 30 tools. The complete catalog and closed input
contracts are in [the interface reference](interfaces.md). `workspace_get`
returns compact Project, branch, exact selected Task/operation and workflow facts;
`work_list(kind=TASK|OPERATION)` provides scoped bounded pages;
`operation_get` reads the copied exact operation target with optional RESULT,
EVIDENCE, ACTIVITY and RUNTIME_REQUESTS. Core remains the sole workflow owner.

Task creation saves intent; `task_review_start` separately authorizes Review.
Review APPROVE freezes the definition and enters IMPLEMENTATION_READY;
`execution_start` starts the current Task's implementation or reserved verification
path. Verification completion still requires Task Acceptance. WAIT causes no
advancement write or continuous GPT polling.

Each reply that needs a Bridge workspace entry calls `workspace_show` once after
necessary reads, carrying the established `aiSessionId`, a stable
`presentationRequestId`, and exact target. Ordinary entries are COLLAPSED;
explicit user requests to show/open the card use EXPANDED. Only
`project_selection_show` allows initial unbound Project selection. Host display
and later model-context delivery require manual host acceptance.

Legacy Task confirmation, legacy execution authorization, runtime
approval decisions, and workflow polling are app-private card actions. They are
not model orchestration tools. The Console receives only bounded terminology
updates: primary summaries prefer readiness and Feature Area/Review/Task/Execution
vocabulary, while durable runtime IDs remain collapsed diagnostics. Review
history includes state, focus, bounded result/AgentClaim, observed-inspection
counts, usage, and a local-only View Prompt/Copy action backed by the exact
persisted prompt. Full prompts never enter the general Logs page or a
model-facing endpoint.

The authoritative product-facing contract—including the frozen Server
Instructions, the 30 tool descriptions, Project Context mapping, product states,
and internal audit reference—is maintained in
[`docs/interfaces.md`](interfaces.md). Historical M3 descriptions above
record implementation evolution; they are not the primary AI-facing workflow
teaching surface.

## FeatureArea ownership closure (schema v28)
