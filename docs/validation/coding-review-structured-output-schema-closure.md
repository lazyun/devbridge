# Coding Review structured output schema closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-d92de8e3e2c37878)。

## Baseline and failure

The work started on branch `dev_20260908_1913` at
`f9a11ef6cb6c0eeaf72e47ad641e155f2f57cc1b`, schema v84, with an empty
index. The opening non-ignored-file manifest is
`/private/tmp/coding-review-schema-opening.sha256` (manifest SHA-256
`a583c5505a0bd1b53892506041acc49381c0ac337e96a8d6a65fe4b6a84673a3`).

The production Coding Review schema contained two unconstrained nested object
schemas: `repositoryTargets.items` and `verificationSpec.checks.items`. The
audit also found a missing closed-object declaration on the Verification v2
root and late-added optional properties in the Recovery Status root that were
not represented as required nullable values. The Codex client checked only
JSON syntax, so an invalid strict schema could reach `turn/start` and the
Responses API could reject it before generation.

## Closed contract

`internal/structuredschema` is the shared builder and final-byte validator for
Codex structured output schemas. Its object builder always emits a closed
object, requires every declared property, and uses an explicit nullable union
when a value may be absent. Its validator recursively examines objects in
properties, arrays, composition branches, and definitions.

The following production owners now pass through that contract:

- Coding Review implementation/findings result;
- Implementation result;
- Verification v2 report;
- Project Inspection report;
- Recovery Status and any other caller at the final Codex `StartTurn` boundary.

The Coding Review schema models every repository target, read/write scope,
execution scope, and verification check field explicitly. The current
`review.ResultJSONSchema` SHA-256 is
`0ca5b4b641e2686d1335e0f5d2dbd0c2b9be12b45bddf029863de35c06e27dae`.

The execution coordinator validates its selected schema before persisting the
prompt or marking a conversation turn pending. The Codex client validates the
exact serialized bytes again before runtime binding, session creation, turn
claim, or RPC dispatch. A malformed schema therefore produces local
`INVALID_OUTPUT_SCHEMA` infrastructure failure without an App Server Turn or
model request.

## Verification

The focused schema, Review, Inspection, Codex client, execution, and probe
packages pass. The production Coding Review facade integration uses a new
test-owned Task, Review, Run, managed worktrees, and fake App Server; it asserts
the exact strict schema on the wire, a successful authoritative Run, and the
persisted Review result. It does not call a model or touch the historical live
Review, Run, Thread, Turn, runtime, DataRoot, Repository, or worktree named in
the incident report.
