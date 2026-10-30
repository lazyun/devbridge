# 历史验收记录

这里按主题保存 Dev Bridge 的历史交付、检查和验收报告。报告中的 PASS、SKIP、NOT_RUN、BLOCKED、版本号与证据范围维持原有含义；这些记录不证明当前工作区通过了相同检查。

当前产品使用方式见[项目首页](../../README.md)和[用户指南](../user-guide/README.md)；当前验证命令见[开发与测试](../../README.md#开发与测试)。`docs/testing/` 保存可复跑验证说明，本目录保存历次结果。

## 新增验证记录

- [Codex 独立验证（2026-10-21）](codex-verification-2026-10-21.md)：GPT 文件实施后的验证 Run、命令证据、重启与验收、数据库升级和回归限制。

- [标签页操作保留当前展示（2026-10-21）](sticky-task-view-2026-10-21.md)：分析、审批、取消和任务终态均不自动返回对话卡片。

- [等待审查审批操作入口（2026-10-21）](review-approval-actions-2026-10-21.md)：审批确认、修订与显式任务取消入口及作用范围验证。

- [审查完成后点击 GPT 分析（2026-10-21）](review-result-analysis-click-2026-10-21.md)：修正通用状态查询为准确 Review 报告分析。

- [任务卡片点击唤起 ChatGPT（2026-10-21）](task-card-conversation-2026-10-21.md)：记录显式点击消息、只读任务范围与失败回退。

- [ChatGPT PiP 任务进度接入（2026-10-21）](task-progress-pip-2026-10-21.md)：记录悬浮视图、普通对话回退与显示生命周期验证。
- [execution_prepare 参数与验证失败诊断（2026-10-21）](execution-prepare-contract-2026-10-21.md)：记录参数层级、Schema 对齐与必需静态检查的准入限制。
- [Task Events、Codex 历史与普通对话兼容（2026-10-21）](task-events-and-codex-history-2026-10-21.md)：记录可选增强的本地结果及真实宿主验收边界。
- [任务审查方式与无活动提示（2026-10-21）](task-review-choice-and-inactivity-2026-10-21.md)：记录本轮定向检查和全量回归限制。

## 整理规则

- 报告和截图保持原路径，便于浏览与外部引用；按下面的主题索引查找。
- 原始日志、JSON 快照、补丁及辅助脚本按原验收批次压缩保存，报告中的证据链接指向对应归档的文件索引。
- 归档同时保留整理前的原报告与截图，避免导航修订改变原批次证据。每个文件和每个压缩包都有 SHA-256；零字节日志及内容相同的前后快照仍保留原身份。
- 不执行归档里的历史脚本，也不把仅存在于旧 `/private/tmp` 或本地 `skeleton/` 的材料描述为随仓库提供。历史文本中的旧路径仅用于追溯。

本次整理保留 60 份可读报告，建立 23 个完整原始归档，收纳 275 个原先散落的非报告、非图片文件。

验证归档完整性（只读，不解包或执行其中脚本）：

```sh
python3 scripts/verify-validation-archives.py
```

## 运行时、恢复与运行资格

| 报告 | 路径 |
| --- | --- |
| [AGENT_EXECUTION_OWNERSHIP_UPGRADE_CLOSURE_READY](agent-execution-ownership-closure.md) | `agent-execution-ownership-closure.md` |
| [Agent Execution ownership upgrade — validation](agent-execution-ownership-upgrade.md) | `agent-execution-ownership-upgrade.md` |
| [Codex 0.159.2 运行中重连验收](codex-01592-active-turn-continuity-2026-10-02.md) | `codex-01592-active-turn-continuity-2026-10-02.md` |
| [Post-Orphan Recovery Acceptance + Git Delivery Closure v46](post-orphan-recovery-closure/final-report.md) | `post-orphan-recovery-closure/final-report.md` |
| [Validation manifest](post-orphan-recovery-closure/validation-manifest.md) | `post-orphan-recovery-closure/validation-manifest.md` |
| [Runtime request payload schema closure](runtime-request-payload-schema.md) | `runtime-request-payload-schema.md` |
| [Frozen Skeleton：显式启动、确认终态与审查并发准备](skeleton-frozen-approval-selection-completion-2026-10-01.md) | `skeleton-frozen-approval-selection-completion-2026-10-01.md` |
| [Frozen Skeleton：定义审批的不可变归档](skeleton-frozen-definition-approval-archive-2026-10-02.md) | `skeleton-frozen-definition-approval-archive-2026-10-02.md` |
| [Frozen Skeleton：无 Run 修订出口与 Task 取消](skeleton-frozen-no-run-revision-2026-10-01.md) | `skeleton-frozen-no-run-revision-2026-10-01.md` |
| [Frozen Skeleton：取消接续与交付回执生产修复](skeleton-frozen-production-recovery-2026-10-01.md) | `skeleton-frozen-production-recovery-2026-10-01.md` |
| [冻结约束的运行资格与正式执行闭环](skeleton-frozen-remaining-runtime-constraints-2026-10-02.md) | `skeleton-frozen-remaining-runtime-constraints-2026-10-02.md` |
| [运行约束后续审核与修复（2026-10-02）](skeleton-frozen-runtime-followup-2026-10-02.md) | `skeleton-frozen-runtime-followup-2026-10-02.md` |
| [运行约束缺陷修复验收（2026-10-02）](skeleton-frozen-runtime-gap-fixes-2026-10-02.md) | `skeleton-frozen-runtime-gap-fixes-2026-10-02.md` |
| [Frozen Skeleton：运行平台资格的持久化基础](skeleton-frozen-runtime-platform-foundation-2026-10-01.md) | `skeleton-frozen-runtime-platform-foundation-2026-10-01.md` |

## 任务、审查与交付

| 报告 | 路径 |
| --- | --- |
| [Approval → automatic first Preparation closure](approval-to-preparation-automatic-continuation.md) | `approval-to-preparation-automatic-continuation.md` |
| [Coding Review structured output schema closure](coding-review-structured-output-schema-closure.md) | `coding-review-structured-output-schema-closure.md` |
| [Dogfood Workflow Closure](dogfood-workflow-closure.md) | `dogfood-workflow-closure.md` |
| [FeatureArea 多 Task 与 canonical Workspace 验收](feature-area-continuation-v0.1.md) | `feature-area-continuation-v0.1.md` |
| [FeatureArea 创建与首次 Task-owned Review 组合验证](feature-area-create-and-review-composition.md) | `feature-area-create-and-review-composition.md` |
| [Feature Area Create and Initial Review Composition: manual GPT evaluation](feature-area-create-and-review-manual.md) | `feature-area-create-and-review-manual.md` |
| [Invalid Review Result Resolution and Target Identity](invalid-review-result-resolution-target-identity-closure.md) | `invalid-review-result-resolution-target-identity-closure.md` |
| [REMOVE_TASK_PREPARATION_FROM_V0_1_ARCHITECTURE](remove-task-preparation-v0.1.md) | `remove-task-preparation-v0.1.md` |
| [REVIEW_FAST_DEEP_ARCHITECTURE_CLOSURE 交付报告](review-fast-deep/report.md) | `review-fast-deep/report.md` |
| [Same-Task Re-review Auto-start Closure](same-task-re-review-auto-start-closure.md) | `same-task-re-review-auto-start-closure.md` |
| [Change inventory](unified-review-executor/changed-files.md) | `unified-review-executor/changed-files.md` |
| [UNIFIED_REVIEW_EXECUTOR_AND_UPPER_LLM_RESULT_CLOSURE](unified-review-executor/report.md) | `unified-review-executor/report.md` |
| [项目公开流程闭环验收](usable-project-public-workflow-2026-10-02.md) | `usable-project-public-workflow-2026-10-02.md` |

## 确认、授权与作用域

| 报告 | 路径 |
| --- | --- |
| [New Confirmation intent gets its own Card instance — verification and closure](confirmation-new-intent-card-instance-identity-closure.md) | `confirmation-new-intent-card-instance-identity-closure.md` |
| [Confirmation prepare atomicity, exact readback, Host mount gate and Card entry binding — final verification and closure](confirmation-prepare-atomicity-and-host-mount-gate-final-closure.md) | `confirmation-prepare-atomicity-and-host-mount-gate-final-closure.md` |
| [Rejected-not-consumed manual retry, exact read identity and replacement version semantics — verification and closure](confirmation-rejected-not-consumed-retry-and-read-identity-closure.md) | `confirmation-rejected-not-consumed-retry-and-read-identity-closure.md` |
| [Confirmation typed rejection, readback and stale-intent validation](confirmation-typed-rejection-readback-stale-intent-closure.md) | `confirmation-typed-rejection-readback-stale-intent-closure.md` |
| [Current Decision authority presentation closure](current-decision-authority-presentation-closure.md) | `current-decision-authority-presentation-closure.md` |
| [DIRECT_FILE_CAPABILITY_V0_1_CLOSURE](direct-file-capability/report.md) | `direct-file-capability/report.md` |
| [INVALID_REVIEW_RESULT_DECISION_CONSUMPTION_AND_CONFIRMATION_REPREPARE_PARITY_CLOSURE](invalid-review-result-decision-consumption-and-confirmation-reprepare-parity-closure.md) | `invalid-review-result-decision-consumption-and-confirmation-reprepare-parity-closure.md` |
| [Pending Decisions 作用域隔离](pending-decisions-scope-isolation.md) | `pending-decisions-scope-isolation.md` |
| [Review Approval minimal authorization surface](review-approval-minimal-authorization.md) | `review-approval-minimal-authorization.md` |
| [Review confirmation stage and auto-execution validation](review-confirmation-stage-auto-execution-closure.md) | `review-confirmation-stage-auto-execution-closure.md` |

## ChatGPT、卡片与 Web 界面

| 报告 | 路径 |
| --- | --- |
| [Card and Details product polish closure](card-and-details-product-polish-closure.md) | `card-and-details-product-polish-closure.md` |
| [ChatGPT 卡片展示流程交付报告](chatgpt-card-presentation-flow-closure.md) | `chatgpt-card-presentation-flow-closure.md` |
| [ChatGPT card presentation manual evaluation](chatgpt-card-presentation-manual-evaluation.md) | `chatgpt-card-presentation-manual-evaluation.md` |
| [ChatGPT Host capability diagnostics](chatgpt-host-capability-diagnostics.md) | `chatgpt-host-capability-diagnostics.md` |
| [Review wakeup：model-context update 可选增强](chatgpt-review-wakeup-context-optionality.md) | `chatgpt-review-wakeup-context-optionality.md` |
| [Review wakeup fallback：调用签名证据缺口](chatgpt-review-wakeup-fallback-signature-gap.md) | `chatgpt-review-wakeup-fallback-signature-gap.md` |
| [Review wakeup capability fallback](chatgpt-review-wakeup-fallback.md) | `chatgpt-review-wakeup-fallback.md` |
| [CHATGPT_TASK_CENTRIC_CARD_UI_CLOSURE](chatgpt-task-centric-card.md) | `chatgpt-task-centric-card.md` |
| [Dev Bridge Card visual design polish](dev-bridge-card-visual-design-polish.md) | `dev-bridge-card-visual-design-polish.md` |
| [DEV_BRIDGE_CARD_VISUAL_TEMPLATE_INTEGRATION_READY](dev-bridge-card-visual-template-integration.md) | `dev-bridge-card-visual-template-integration.md` |
| [Modern Workspace Card presentation](dev-bridge-modern-workspace-card.md) | `dev-bridge-modern-workspace-card.md` |
| [GPT Surface Consolidation and Collapsible Card V1 候选交付](gpt-surface-consolidation-v1.md) | `gpt-surface-consolidation-v1.md` |
| [Legacy JavaScript and Card fixture closure](legacy-javascript-card-fixture-closure.md) | `legacy-javascript-card-fixture-closure.md` |
| [MOUNTED_CARD_REVIEW_COMPLETION_GPT_WAKEUP_CLOSURE_READY](mounted-card-review-completion-gpt-wakeup.md) | `mounted-card-review-completion-gpt-wakeup.md` |
| [REVIEW_OBSERVABILITY_AND_CARD_LIVE_UPDATE_CLOSURE_READY](review-observability-and-card-live-update-closure.md) | `review-observability-and-card-live-update-closure.md` |
| [Review wakeup late claim ordering closure](review-wakeup-late-claim.md) | `review-wakeup-late-claim.md` |
| [Runtime Config + Card Approval + Worktree UX Closure](runtime-config-card-worktree-closure/report.md) | `runtime-config-card-worktree-closure/report.md` |
| [TASK_FIRST_WEBUI_CARD_V0_1_READY](task-first-webui-card-v0.1.md) | `task-first-webui-card-v0.1.md` |
| [WEBUI_TASK_CENTRIC_WORKBENCH_REDESIGN_V1](webui-task-centric-workbench.md) | `webui-task-centric-workbench.md` |

## 版本汇总与测试夹具

| 报告 | 路径 |
| --- | --- |
| [Legacy Go broad fixture final closure ledger](legacy-go-broad-fixture-final-closure.md) | `legacy-go-broad-fixture-final-closure.md` |
| [v0.1-current-workspace-inventory-delivery](v0.1-current-workspace-inventory-delivery.md) | `v0.1-current-workspace-inventory-delivery.md` |
| [V0.1 Final Acceptance Remaining Closure](v0.1-final-remaining-closure/final-report.md) | `v0.1-final-remaining-closure/final-report.md` |
| [Validation manifest — v0.1 final remaining closure](v0.1-final-remaining-closure/validation-manifest.md) | `v0.1-final-remaining-closure/validation-manifest.md` |

## 原始批次归档

| 批次 | 原始文件数 | 压缩后字节数 |
| --- | ---: | ---: |
| [chatgpt-card-presentation-flow-closure](archive/chatgpt-card-presentation-flow-closure.md) | 14 | 11,311 |
| [chatgpt-host-capability-diagnostics](archive/chatgpt-host-capability-diagnostics.md) | 5 | 13,876 |
| [chatgpt-review-wakeup-context-optionality](archive/chatgpt-review-wakeup-context-optionality.md) | 6 | 14,758 |
| [chatgpt-review-wakeup-fallback](archive/chatgpt-review-wakeup-fallback.md) | 6 | 14,385 |
| [chatgpt-task-centric-card](archive/chatgpt-task-centric-card.md) | 4 | 19,123 |
| [current-decision-authority](archive/current-decision-authority.md) | 6 | 18,254 |
| [direct-file-capability](archive/direct-file-capability.md) | 14 | 11,111 |
| [feature-area-continuation-v0.1](archive/feature-area-continuation-v0.1.md) | 19 | 30,669 |
| [feature-area-create-and-review-composition](archive/feature-area-create-and-review-composition.md) | 12 | 19,391 |
| [pending-decisions-scope-isolation](archive/pending-decisions-scope-isolation.md) | 4 | 12,245 |
| [post-orphan-recovery-closure](archive/post-orphan-recovery-closure.md) | 22 | 114,275 |
| [prompt-neutrality](archive/prompt-neutrality.md) | 8 | 13,250 |
| [remove-task-preparation-v0.1](archive/remove-task-preparation-v0.1.md) | 13 | 23,194 |
| [review-approval-minimal-authorization](archive/review-approval-minimal-authorization.md) | 3 | 16,041 |
| [review-fast-deep](archive/review-fast-deep.md) | 17 | 194,567 |
| [review-wakeup-late-claim](archive/review-wakeup-late-claim.md) | 6 | 15,010 |
| [runtime-config-card-worktree-closure](archive/runtime-config-card-worktree-closure.md) | 18 | 24,870 |
| [task-first-webui-card-v0.1](archive/task-first-webui-card-v0.1.md) | 21 | 299,248 |
| [unified-review-executor](archive/unified-review-executor.md) | 15 | 17,896 |
| [v0.1-final-remaining-closure](archive/v0.1-final-remaining-closure.md) | 48 | 693,821 |
| [webui-task-centric-workbench](archive/webui-task-centric-workbench.md) | 21 | 23,233 |
| [webui-task-owned](archive/webui-task-owned.md) | 13 | 314,111 |
| [报告整理前原文](archive/historical-reports.md) | 52 | 207,367 |

[完整校验清单](archive/manifest.json)同时记录原始路径、字节数、权限与 SHA-256。归档数据没有重新运行，也没有据此更新旧验收结论。
