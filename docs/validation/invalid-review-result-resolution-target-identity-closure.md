# Invalid Review Result Resolution and Target Identity

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-35f890c99ad18dc2)。

Schema v87 adds an immutable resolution receipt for Coding Review results whose
raw structured output cannot become a formal report. The original Run, raw
bytes/hash, materialization outcome and reason remain unchanged. Core exposes
one dedicated `INVALID_REVIEW_RESULT_RESOLUTION` Decision with three product
actions: request a same-Task successor Review, open Project Repository
management, or abandon the Task.

`REQUEST_RE_REVIEW` accepts only bounded immutable feedback. In the same writer
transaction it consumes the exact invalid-result Decision, revalidates the
current Task/Review and frozen Workspace members, creates ordinal + 1 on the
same Task, reserves one Run/start authority, and records the source raw-result
hash. The existing continuation worker starts that reservation. Exact replay
returns the same successor; changed feedback conflicts.

Coding Agent output uses closed aliases (`target-1`, `target-2`, …). The model
schema contains no repository, member, version, origin, development-cycle or
primary-member identity. During materialization Core maps each alias to the
exact frozen Review target, injects those identities and read authorization,
and validates any proposed write scope. Unknown/duplicate aliases and any
model-authored authority key are retained as INVALID raw history. Repository
needs outside the closed aliases belong in limitations or blockers.

Upper-LLM Review submission remains a formal Core-facing report contract and is
not parsed as the Coding Agent alias wire DTO.
