> Historical validation record. Describes the implementation before the final v0.1 model; it is not a current architecture or interface contract. See [current model](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# Dogfood Workflow Closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-d7053f806fbdd13b)。

Starting main: `73864ffca5da5d78a562b3e6c2ca405cb1650f12`, clean.
Implementation branch: `codex/dogfood-workflow-closure` in an independent worktree.
Observed origin/main: `c54153457b887593b2fd28c6fed8056cede8051c`.

## Product behavior

- Preparation collections remain verbatim engineering facts. Empty paths,
  annotated descriptions, overlap with do-not-modify observations, and newly
  discovered modules do not become authorization gates.
- A successful analysis is READY. An agent's original CONFLICT is retained in
  `reportedVerdict` and the immutable Run claim. Bridge CONFLICT is based on
  deterministic repository provenance; runtime/analysis failures remain FAILED.
- Execution no longer rejects changed files outside an authored semantic scope.
  Repository/workspace ownership, sandbox, FeatureArea claim, runtime epoch,
  user authorization and Git/release controls remain enforced.
- ScopeExpansion proposals and decision tools are removed from active tool
  discovery; production mutation is rejected. Legacy rows and migrations remain
  readable, and old pending rows do not gate active workflow.
- Shared GPT/local-MCP `task_prepare` accepts an existing Task ID, an optional
  exact TaskDefinitionRevision ID, and a verbatim preparation instruction.
  Omission captures the current revision identity. Every call creates a distinct
  attempt, with monotonically increasing ordinal and unchanged prior history.
- Repository HEAD belongs to the attempt. Subsequent attempts use separate
  Task-owned workspaces; execution binds to the explicitly selected attempt's
  baseline and environment. Renamed files do not invalidate authored metadata.
- Review/Preparation/Execution remain serialized within a FeatureArea. Reviews
  in different FeatureAreas can run simultaneously within one Project/session.
- Review completion/result delivery uses Review ID, Run ID, FeatureArea ID and
  the original requesting interaction. Moving workflow focus does not retarget
  the result. Continuation deduplication includes immutable attempt identity.
- `project_open` returns Project Context and FeatureArea inventory at Project
  scope. `workflow_get(featureAreaId)` supports explicit area reads. No durable
  selected-FeatureArea state model was added.
- Console renders the durable Preparation reason verbatim beside its status,
  preserving line breaks and escaping HTML. Task metadata description occupies
  a separate full-width row without negative margins or absolute positioning.

## Regression evidence

`TestDogfoodCrossFeatureAreaReviewsAndOriginalCompletionCorrelation` uses two
MCP adapters, one shared Project/session, actual SQLite claims, supervisor,
managed worktrees and a barrier-controlled fake Codex. A and B are both RUNNING;
same-area Preparation is busy; creating/reading C does not change A's completion
or callback identity. C remains unpolluted. The existing SQLite operation-pair
race covers same-area Review/Preparation/Execution combinations.

`TestDogfoodModelConfigurationRePreparationPreservesFactsAndHistory` replays the
actual persisted analysis for “Make coding-agent model support dynamically
configurable”: 17 modification-path descriptions, 15 read dependencies,
7 interfaces, 7 risks and 10 verification targets. Validators, SQLite migration,
Console and MCP schema expansion descriptions remain intact, with READY status.
Three attempts share one Task and immutable definition. A repository rename
between attempts and an explicit same-state re-analysis both succeed. No
implementation is created by Preparation. A separate synthetic user-decision
subtest confirms execution uses the selected attempt's workspace and new HEAD.

The optional `TestDogfoodPersistedTaskSnapshot` was run against a consistent,
isolated copy of the actual dogfood database. On the actual Task identity
`task_d163e9454ab4f6c08fd4f5c478123add`, new Preparations #2 and #3 became READY
using fake Codex replay. The original #1 CONFLICT remained unchanged across all
decoded durable fields. The live database was not modified. This is a persisted
real-data regression with a synthetic backend, not a claim that the live Task
was rerun using real Codex.

The optional bounded real read-only Review used real Codex (`gpt-5.6-sol`, low)
on one synthetic repository file. Review
`review_5a4ee52e2e383ded6fe886285a4e3611`, Run
`run_c56e0f519f031c020fd90d13cbda8c7a` succeeded in about 13 seconds. The durable
Evidence store confirmed zero changed files and a nonempty repository baseline.
No Task or implementation Execution was created.

Architecture guards cover fact-only handling, retired active scope tools and
Upper AI decision ownership. Existing runtime safety, recovery, epoch, ownership,
immutable revision and host-contract suites remain part of full validation.
The Console was also inspected in the in-app browser using production render
functions and CSS: the description starts 14 px below the metadata grid and the
multiline blocker appears verbatim.

## Validation

Passed: root `go test ./...`, `go test -race -timeout 30m ./...`, and
`go vet ./...`; Desktop Go test/race/vet; Console/frontend tests; frontend
production build; `./build-desktop.sh`; and `git diff --check`. The initial root race invocation hit Go's default ten-minute
package timeout while the SQLite suite was executing repeated migrations; no
data race was reported. The complete suite was rerun with a bounded thirty-minute
package timeout and passed, with SQLite completing in approximately 600 seconds.
The final task report records the merge commit identity and main verification. No push is part of this workflow.
