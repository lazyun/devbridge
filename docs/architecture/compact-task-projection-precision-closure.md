# Compact Task projection precision closure

Status: `COMPACT_TASK_PROJECTION_PRECISION_CLOSED`

Default workspace and Task inventory reads use the Core-owned `workflowpresentation.CompactTaskProjection`. It contains bounded identity and workflow-location facts: Task and owner IDs, title, phase, status, current Review/Execution pointers, current definition revision identity, update time, and `detailsAvailable`.

The compact DTO has no slot for `problemStatement`, goal, constraints, work or verification specifications, implementation instructions, or Review instructions. Those fields are structurally absent from JSON and its generated schema. The former MCP `selectedTask.goal` field was removed because it copied `Task.WorkSpec.Goal` and could therefore carry the complete ProblemStatement under another field name.

`workspace_get` preserves two distinct read levels:

- The default read returns `selectedTask` and workflow facts through the compact DTO. Current/latest Task pointers, Task phase, required action, and available capabilities remain Core-derived.
- Explicit `include=["TASK"]`, with an exact authorized `taskId`, returns the existing exact Task detail and its source partitions. Definition content remains byte-preserving there.

`CompactWorkspaceForSession` still derives capabilities from the exact Core workflow path, then removes exact current/latest `TaskView` objects before returning its compact projection. This prevents `originalRequirement`, work specifications, verification definitions, and Review instructions from leaking through a nested presentation object.

MCP workspace and Task-list responses consume the same Core compact DTO. HTTP/Console, Product Workbench, Desktop embedded workbench, and ChatGPT Card already consume bounded Task summaries or Core current/latest `TaskFact` values for their main workspace surfaces; they do not need a client-side deletion rule. Exact detail surfaces retain their explicit read contracts.

No Task lifecycle, admission, current/latest selection, confirmation, database, or write-side behavior changed.
