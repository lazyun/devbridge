# Feature Area Create and Initial Review Composition: manual GPT evaluation

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-70fa58b3e50361eb)。

Status: **NOT_RUN**. These cases require a real GPT host conversation after automated
protocol and fake-backend coverage; they must not be run against the existing Feature
Area `feature_area_2484b097e0a22782bc6d7c561315bfc9` or a live production database.

| Case | User scenario | Expected tool sequence and stopping point | Status |
| --- | --- | --- | --- |
| 1 | “创建领域修复此已说明问题，并开始只读调查”；目标、Repository、focus、Task 与 Review 参数齐全 | `feature_area_create(mode=CREATE_AND_REVIEW)` once, then one final `workspace_show`; no unexplained stop after area creation | NOT_RUN |
| 2 | “先建领域记录下来，暂不审查” | `feature_area_create(mode=CREATE_ONLY)` only; the `CREATE_TASK` guidance does not start Task/Review | NOT_RUN |
| 3 | 已明确现有领域，出现新问题 | Reuse exact FeatureArea; `task_create` then `task_review_start`; no duplicate FeatureArea | NOT_RUN |
| 4 | 已明确现有 Task，尚未 Review | `task_review_start` for exact Task; no duplicate Task | NOT_RUN |
| 5 | 请求调查但缺 Repository、只读授权、完整 Review 输入或有效开发上下文 | Explain the exact blocker; no guessed parameter and no FeatureArea/Task/Review creation | NOT_RUN |
| 6 | 组合请求部分失败或响应丢失后使用同一 requestId 重试 | Return/resume the original identities and unfinished step only; no duplicate FeatureArea, Task, Review or Run | NOT_RUN |
| 7 | Review Approval 或 Task Acceptance 已待决，用户历史上曾说“开始修复” | Present the exact pending decision and wait; no approval, implementation, verification or runtime-permission action | NOT_RUN |

Record for each run: exact aiSessionId/Project/FeatureArea/Task/Review/Run IDs,
root requestId, tool call order, returned composition startup fields, final Card call
count, and whether the host treated a display or `nextAction` as authorization. A
successful automated test does not prove that GPT will never stop prematurely in a
real host.
