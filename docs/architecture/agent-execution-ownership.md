# Agent operation identity and Task ownership

The [Task-owned v46 contract](task-execution-v0.1.md) defines semantic progression.
Review and Execution belong to the same engineering Task. The agent_executions
identity envelope provides common runtime diagnostics and exact cancellation;
a Review envelope does not introduce a second semantic Execution cycle or Task.
Project-scope Inspection retains its internal operation representation.

| Runtime envelope | Semantic owner | Run purposes |
| --- | --- | --- |
| REVIEW | Exact Task-owned Review | SCOPE_REVIEW, one linked Run |
| DEVELOPMENT | Exact Task Execution cycle | At most one IMPLEMENTATION and one VERIFICATION |
| INSPECTION | Project ContextProbe | INSPECTION |

## Admission and dispatch

`runs.agent_execution_id` is an immutable foreign key. INSERT guards reject
missing identity and mismatched Task, Project, FeatureArea, semantic owner,
authorization, or purpose. Existing owner columns, runtime epoch, claim guards,
finalization, cancellation, and orphan fences remain authoritative.

Coordinator validates durable ownership at entry and revalidates before each
thread/start, thread/resume, and turn/start. Dispatch additionally requires the
exact active AgentConversation boundary for FeatureArea work. The independent
Inspection runner validates its Project ownership before every backend handoff.
A compatibility store without the validation capability cannot start a backend.
An unproven entry does not receive cleanup, cancellation, or domain completion
authority merely because validation failed.

## Projection and safety

AgentExecution reduction describes child operation activity, never Task.phase.
Task views and inventory use the semantic Task title, status and durable phase.
The universal execution inventory can retain all three operation envelope kinds.
Exact owner, Run, cancellation and physical backend disposition remain separate.
Unknown backend termination retains quarantine and does not claim the Task failed.

Immutable identity uses WITHOUT ROWID and exact owner uniqueness. INSERT, REPLACE,
IGNORE and UPSERT cannot rebind an existing owner, including before its first Run.
Workspace and AgentConversation remain FeatureArea-owned across Tasks and cycles.
The v46 schema supports no legacy semantic workflow reader or pseudo-Review Task.
