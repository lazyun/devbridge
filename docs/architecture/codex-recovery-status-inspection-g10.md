# Project Inspection Recovery Status contract

Project Inspection uses the shared Recovery Status workflow. The source owner is
`PROJECT_INSPECTION:<inspectionOperationId>` and remains the existing Project
Inspection Task with `scope=PROJECT` and `featureAreaId=NULL`. Recovery never
creates a FeatureArea, TaskDefinitionRevision, Execution, Review, hidden Thread,
or replacement App Server.

The durable relation is:

```text
Project Inspection owner Task
└─ Inspection operation
   ├─ source Inspection Run / terminal Turn
   ├─ Recovery Status child Run / new Turn
   └─ optional RECOVERED_PARTIAL continuation Run / new Turn
```

The source specification binds the Project, owner Task and operation, source
Run and Turn, runtime epoch, Workspace, RuntimeWorkset hash, canonical
conversation and Thread, and App Server instance. The owner kind is the
discriminator. FeatureArea, revision, Execution, and Review identities remain
absent. Historical Inspection rows without the canonical v82 receipt,
conversation, managed Workspace, RuntimeWorkset v2, App Server binding, and
terminal source Turn fail closed as `LEGACY_INSPECTION_NO_CANONICAL_RUNTIME`.

Prepare persists the caller-authored recovery instruction and checksum without
starting a model. Start revalidates the current owner, source terminal fact,
idle exact Thread, no pending request or cancellation, the complete managed
member set, root identities, workset hash, claim, and App Server binding. It
creates a distinct `RECOVERY_STATUS` Run and starts a new Turn on the existing
loaded Thread. Roots, cwd, read-only intent, runtime choice, and output schema
are protocol fields. Core does not alter the instruction bytes or inject the
source prompt, an old question, or an old answer.

Recovery Status report v1 remains byte and hash compatible for existing
FeatureArea owners. Project Inspection uses v2. Its required
`inspectionResult` is validated by the same `internal/inspectionresult` schema
and parser used by the canonical Inspection runtime. The nested source binds
the Project, owner Task, operation, source Run, and RuntimeWorkset hash. Unknown
fields, duplicate keys, prose fallback, missing member observations, paths
outside a confirmed read scope, and source mismatches are rejected.

The shared observer records every managed member before dispatch and after the
Recovery Turn becomes terminal and quiescent. Evidence includes repository and
member identities, managed worktree and canonical root identity, origin and
HEAD, Git snapshot, index/diff and metadata digests, branch, and changed paths.
Mutation, root replacement, member drift, HEAD/index/metadata drift, or missing
members produces conflict or failure. Dev Bridge preserves the evidence and
does not reset or clean a worktree.

The immutable Assessment binds source and child identities, report bytes and
hash, nested Inspection result hash, RuntimeWorkset hash, before/after
observation hash, owner evidence hash, old response disposition, reasons, and
actions. `RECOVERED_COMPLETE` requires a legal complete nested result and
consistent facts. `RECOVERED_PARTIAL` requires a legal blocked result within
the existing targets. An actual new `CODEX_REQUEST` remains pending for an
explicit immutable UserDecision; report prose cannot manufacture a question.
Target or contract expansion yields `REVISION_REQUIRED`.

Resolve consumes only the durable Assessment. Complete resolution enters the
canonical Inspection finalization reducer in the same SQLite transaction and
records `RECOVERY_STATUS` provenance, source and child Runs, report and
assessment receipts, and owner evidence. It does not rewrite source Thread or
Turn history. A source request whose response disposition was `UNKNOWN` remains
`UNKNOWN`; engineering recovery is independent evidence.

Partial resolution leaves the immutable source attempt historical. A separate
caller-authored continuation command references the resolved Recovery record
and carries a stable request ID, exact instruction, payload hash, and expected
epoch. Its immutable receipt creates one new Inspection Run on the same
operation, Task, conversation, App Server, Thread, Workspace, RuntimeWorkset
member set, backend, and target set. It starts a new Turn and never revives the
source Turn. Changed replay payloads and stale epochs fail closed.

`REVISION_REQUIRED` cannot use that continuation. The current target set and
Workspace remain unchanged. A caller must use the existing Project Inspection
prepare/confirm contract to create a formally confirmed successor operation
with its own owner, receipt, Workspace, RuntimeWorkset, conversation, Thread,
and Run. The old report and Assessment remain historical evidence only.

The shared read projection exposes owner kind, Project and owner Task,
operation, source and child Run/Turn, Workspace/workset/member summary, report,
consistency, engineering disposition, old response disposition, reasons,
actions, and timestamps. MCP, HTTP, Console, WebUI, ChatGPT Card, and the
Desktop embedded workbench consume that projection. Reads and refreshes do not
dispatch a Turn. Write DTOs do not accept an outcome, findings, Assessment,
factual admission booleans, or force flag.

Schema v83 adds only the explicit Inspection Recovery source/child fences,
immutable continuation and resolution receipts, the current continuation
pointer, and permits multiple historical runtime records for one immutable
Inspection operation. A resolution receipt binds `RECOVERY_STATUS`, source and
child Run/Turn identities, the canonical Inspection result hash, report hash,
Assessment hash, owner-facts hash, and engineering disposition. It does not
backfill historical Inspection, re-encode RuntimeWorkset v1/v2, or change
RecoveryStatusReport v1.
