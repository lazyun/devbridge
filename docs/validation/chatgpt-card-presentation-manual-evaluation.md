# ChatGPT card presentation manual evaluation

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-7e534e93372e8259)。

Status for every row below: `NOT_RUN`.

Run these only in an isolated candidate with test identities. Do not use a real Task,
Review, Decision, Execution, formal service, or formal SQLite. Record the exact MCP
tool sequence, number of entries, target/session identity, whether a component mounted,
and any missed or unnecessary entry. A host acceptance result proves only that observed
host; it does not prove model behavior in every conversation.

| # | Prompt or event | Expected observable result | Status / evidence |
| --- | --- | --- | --- |
| 1 | “现在任务怎么样了？” with bound exact Task | direct `workspace_show(COLLAPSED)`; no mechanical `workspace_get` first | NOT_RUN |
| 2 | “展示卡片/打开工作区” | direct `workspace_show(EXPANDED)` once | NOT_RUN |
| 3 | One development request that creates a Task and starts Review | required SILENT reads/mutations, then one final collapsed workspace entry | NOT_RUN |
| 4 | Exact Review completion needing approval advice | `operation_get`, necessary exact validation, one exact pending entry; advice waits for approval | NOT_RUN |
| 5 | Card approval where mutation succeeds but readback fails | action is not repeated; component says submitted/refresh failed and retries read only | NOT_RUN |
| 6 | Open a historical approval that is handled/replaced/unknown | historical identity remains; status distinguishes handled, replaced and unknown; no latest redirect | NOT_RUN |
| 7 | Several related internal reads in one answer | at most one final entry, scoped to the exact intended Task/Decision | NOT_RUN |
| 8 | Architecture or prompt-design discussion while a project has pending work | no unsolicited Card | NOT_RUN |
| 9 | “只用文字回答” | SILENT reads/text response; no UI entry | NOT_RUN |
| 10 | UI capability absent or model-context delivery rejected | business/text path remains usable; no claim GPT received a wakeup | NOT_RUN |
| 11 | Initial valid show snapshot then component mount | initial render consumes same result without an unconditional full workspace reread | NOT_RUN |
| 12 | Collapsed component expands, then background poll races an interactive refresh | local fresh read only; no show; latest interactive target wins and loading/focus state is correct | NOT_RUN |

For a completion notification, also record whether `ui/update-model-context` merely
acknowledged context and whether a separate follow-up request was accepted. Neither is
evidence that GPT executed a follow-up. Verify that completion updates an already
mounted component in place, preserves collapsed/expanded state, and does not emit a
duplicate notification after remount or from a historical entry.
