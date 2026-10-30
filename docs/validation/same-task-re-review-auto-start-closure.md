# Same-Task Re-review Auto-start Closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-e74483cd14b9d4e7)。

The canonical `REVISE` machine action implements the product command
`REQUEST_RE_REVIEW`. The command accepts only immutable `reviewFeedback` (valid
UTF-8, non-empty, no NUL, at most 8192 bytes) in addition to the exact current
Task/Review/Decision/request identity. Core derives every successor identity,
target, backend, runtime and ordinal.

Schema v86 adds `review_re_review_requests`. Its immutable row binds the prior
Decision, Review ordinal and formal result hash to the original feedback bytes
and hash, successor Review/Run, and accepted Review start receipt. The same
SQLite writer transaction consumes the pending Decision, creates ordinal + 1 on
the same Task, reserves one Run, updates `current_review_id` and `REVIEWING`, and
writes the start authority. It creates no TaskDefinitionRevision or Execution.

After commit, the bounded Review continuation reads only accepted, current,
unstarted Review/Run reservations. It validates the complete owner tuple and
dispatches through the existing Coding or Upper Review starter. Failed or
unknown dispatch retains that Review and Run for existing recovery; the worker
never creates Review #3.

Exact replay validates the persisted feedback hash and returns the original
successor identities. A changed payload conflicts. The Decision receipt remains
the single concurrency authority for approve, re-review and abandon.

Validation commands and final counts are recorded in the task completion report.
No live DataRoot, user Task/Review/Run, real model, Desktop UI or Git ref was
used or modified.
