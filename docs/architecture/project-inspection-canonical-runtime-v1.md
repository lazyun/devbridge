# Project Inspection canonical runtime v1

Status: production runtime contract implemented on schema v82 and connected to the shared Recovery Status workflow by schema v83. The G10 closure evidence is recorded in [Project Inspection Recovery Status closure](codex-recovery-status-inspection-g10-closure.md).

## Ownership

A new Project Inspection reuses the visible Inspection Task created with the ContextProbe. The Task has `scope=PROJECT`, `featureAreaId=NULL`, and no TaskDefinitionRevision or Execution. Its runtime owner is the concrete operation:

```text
PROJECT_INSPECTION:<contextProbeId>
```

The owner identity is `(projectId, inspectionOwnerTaskId, inspectionOperationId)`. Two inspections in one Project do not share a conversation, claim, workspace, Thread, or Turn.

Schema v82 adds owner variants without changing historical FeatureArea identities. Existing conversations are represented as `FEATURE_AREA:<featureAreaId>`. New Inspection conversations use `PROJECT_INSPECTION:<contextProbeId>`. Project Inspection claims use `project_inspection_agent_claims`; they do not occupy the FeatureArea claim namespace.

## Prepare, confirm, and start

`project_inspection_prepare` and `POST /v1/project-inspections/prepare` only read the bound Project, the explicitly supplied repositories, their Git state, the selected backend, and the model inventory. Callers must supply every target, each target's read scope, and the primary repository. Prepare does not create a Task, ContextProbe, workspace, member, worktree, conversation, claim, Run, event, App Server, Thread, or Turn.

The plan freezes a deterministic operation ID, owner Task ID, stable member IDs, origin commits and refs, exact instruction, backend fingerprint, model, reasoning effort, primary member, request ID, and payload SHA-256. Target order is canonical. No unselected Project repository is added.

`project_inspection_confirm` atomically creates the existing Project-scope owner Task, ContextProbe, and immutable confirmation receipt. `(projectId, requestId)` is unique. The same request and payload reads the original object; a changed payload conflicts.

`project_inspection_start` requires the session, Project, owner Task, operation, expected owner state, request ID, and payload hash. Optional expected epoch and workset hash fence replays after allocation. Core reloads the receipt and does not accept replacement targets, backend, model, instruction, or outcome from the client. Legacy ContextProbe create/start write endpoints reject new runtime work.

## Resource and runtime allocation

Start first reserves the single Inspection Run in `PREPARING` with the Project Inspection claim and canonical conversation. This ordering is required by the existing worktree foreign key: every managed worktree has a real Run owner. The reservation has no model or App Server side effect.

The materializer then creates or replays the deterministic Project Inspection workspace and its exact confirmed members. Dev Bridge creates managed worktrees at the confirmed commits and attaches their metadata. Partial preparation reuses the same workspace, members, worktrees, and Run. Original registered checkouts are inventory sources only and are never runtime roots.

RuntimeWorkset protocol v2 has owner `PROJECT_INSPECTION`. It freezes the owner Task and operation, Run, workspace, primary member, ordered member bindings, backend fingerprint, model, reasoning effort, service tier, adapter/protocol identity, and content hash. Each member freezes repository and alias, member/version, managed worktree and canonical root identities, origin and start Git facts, exact intended read scope, and `write=NONE`. It carries no FeatureArea, Review, TaskDefinitionRevision, or Execution identity. Protocol v1 validation and hash material remain unchanged.

Only after workspace preparation does Core atomically bind RuntimeWorkset v2, repository bindings, start observations, and the strict output schema to the reserved Run. App Server, Thread, and Turn effects happen after that durable boundary.

## App Server, conversation, and Turn

The runner launches a verified App Server instance with a durable `AppServerInstanceBinding`, then records Thread-start intent before `thread/start` and Turn-start intent before `turn/start`. A successful callback binds the exact Thread or Turn to the Run, ContextProbe, operation boundary, and canonical conversation.

The caller's confirmed instruction bytes are the `turn/start` prompt. Core does not prefix repository lists, cwd text, or safety prose. Runtime roots come from the frozen workset, and the primary member alone selects cwd. The strict Project Inspection JSON schema is supplied through the protocol output-schema field.

On Core restart, preparation resumes the same reserved Run. An active Turn reconnects to the bound App Server and rejoins the exact Thread and Turn. A pending start disposition is fail-closed and is not redispatched. A missing App Server binding becomes recovery required; Core does not create S2 as a substitute for S1.

CODEX_REQUEST continues to use the durable mandatory-user-response flow. Pending requests reattach to the same Run and Thread. GPT cannot answer them, `RUNTIME_APPROVAL` remains separate, and an unknown answer disposition is never resent.

## Result reducer and observations

The only successful path is:

```text
terminal Turn
→ backend idle/quiesced
→ strict structured report
→ exact end observations
→ owner/epoch/Thread/Turn/workset/report validation
→ atomic result, operation boundary, conversation boundary, claim finalization
```

The report identifies the Project Inspection source and includes status, summary, repository-qualified findings, per-repository observations and relevant paths, blocked reasons, cross-repository relationships, and a recommended next action. Unknown fields and prose fallback are rejected. Recommendations are data; they never create FeatureAreas, Tasks, Reviews, revisions, or Executions.

Core compares every managed member's root identity, HEAD, snapshot, index/diff and metadata observations. Any mutation produces `SCOPE_CONFLICT`; Core records the evidence and does not reset or clean the worktree. Cancellation, unresolved runtime requests, stale epochs, mismatched callbacks, unproven quiescence, and changed report/workset identities fail closed. Replaying an already persisted finalization reads the same receipt.

## Compatibility and Recovery source

Historical Inspection rows remain readable without invented evidence. A row without the v1 confirmation receipt, managed workspace, workset, conversation, and App Server facts is classified `LEGACY_INSPECTION_NO_CANONICAL_RUNTIME`. It cannot resume and cannot serve as a Recovery Status source. The user starts a new canonical Inspection instead.

A terminal canonical Inspection exposes the real owner, source Run and Turn, runtime epoch, RuntimeWorkset, conversation, App Server binding, workspace observations, structured report, cancellation, quiescence, and finalization facts used by the shared Recovery Status child. The child keeps the same Project Inspection operation, conversation, App Server and Thread, creates a new Run and Turn, and resolves through the canonical Inspection reducer. A verified partial result can create one receipt-bound continuation Run without changing the historical source Run or target set.
