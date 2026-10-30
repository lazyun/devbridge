# Task 自动提交与 FeatureArea 全量提交交付记录

> Historical implementation evidence. [GPT surface consolidation V1](architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

1. **Starting HEAD**：`0b4d55b3e8f56ba4e805098990097048f0892bb7`，branch `dev_20260908_1913`。开始时工作区干净。

2. **Schema**：v43。fresh bootstrap 使用 `schema_v43.sql`；v42→v43 只增加 nullable checkpoint、内部 receipt 和必要约束，不推断或回填 Git 历史。没有新增持久化 commit status enum、UNKNOWN/LEGACY 提交状态或历史双读路径。

3. **Task auto-commit contract**：仅 Development Task 的 authoritative ACCEPT durable 成功后自动提交。Verification Run SUCCEEDED、报告事实和 AgentClaim 均不会单独触发提交。REQUEST_CHANGES、REQUEST_REVERIFICATION、REJECT 不提交；后续验收仍沿用现有唯一 reducer。

4. **ACCEPT → commit 时序**：在同一 SQLite 事务中写 acceptance receipt、Task SUCCEEDED 和 Task commit receipt；事务提交并释放后执行 Core Git finalization。Git 完成后另一个事务写 Task checkpoint 和 receipt completion。Git 失败不会回滚已完成的验收。

5. **Snapshot fence**：绑定验收 Execution 的终态 Run、immutable revision、Run workspace 和 Verification end tree。canonical workspace 必须仍是同一环境且没有 Git sequencer。fresh snapshot 必须等于已验收 tree；index 允许 HEAD tree 或已完整 staged 的 accepted tree。执行 `git add -A`、`git diff --cached --check`，再次比较 stage tree 与 fresh tree；最终用由 accepted tree 建立的独立 index 执行普通 Git commit，避免另一个 index 写入改变提交内容。禁用 commit hooks 和签名，保留可读 subject 和 Task/revision trailers；无改动验收可形成 empty commit。

6. **Task recovery**：receipt 保存 Task、revision、workspace、verified snapshot、pre-commit HEAD、resulting commit、completion 和失败信息。Git 成功而 DB 未完成时，重试核对 parent、tree、完整 message/trailers 和 clean workspace，补齐原 SHA；不能证明时停止。snapshot drift 返回/记录 `TASK_COMMIT_SNAPSHOT_DRIFT`；不完整提交返回 `TASK_COMMIT_INCOMPLETE`。Task 保持 SUCCEEDED + UNCOMMITTED，阻止新的 Development/Agent operation。`task_commit_retry` 只恢复已验收的 Task，不重跑 Development、Verification 或 Acceptance。

7. **FeatureArea contract**：唯一人工提交操作 `featureArea_commit`，Web 对应 action `COMMIT_FEATURE_AREA`。全量处理当前 canonical branch，不新建 Task、Review、Execution、Commit 或 Integration 领域实体。前置检查 lifecycle、claims、运行中 Run、pending Review decision、Task acceptance、recovery/cancellation、Task checkpoints、repository/ref、两边 clean 和 Git operation。待合入 Task 的 commit 必须是 source HEAD 的 ancestor。失败/取消且未形成合法 checkpoint 的 Development Task 会 fail closed。

8. **FF / merge / conflict**：source=target 成功 no-op；可 FF 使用 `git merge --ff-only`；分叉且有共同祖先使用普通 `git merge --no-edit`。冲突执行 `git merge --abort` 并核对 dev HEAD/clean 恢复，返回 `MERGE_CONFLICT`，不更新 Task 或 FeatureArea merged fields。冲突 receipt 保存失败结果；同 requestId 重放返回原错误。没有自动 rebase、cherry-pick、ours/theirs 或 Coding Agent 解冲突。

9. **Workspace sync**：dev 合入后 canonical FeatureArea branch 执行 `git merge --ff-only <newDevHEAD>`，断言两边 HEAD 一致且 clean 后再落 merged checkpoints。保留 canonical workspace；沿用既有 execution/build resource ownership 清理，不增加 `git clean` 或 `reset --hard`。Git 已完成但 DB finalization 失败时 receipt 恢复原结果，避免第二次 merge commit；也覆盖 dev 已包含 source 的 no-op recovery。

10. **Durable fields**：Development Task：`commit_sha / committed_at / merged_commit / merged_at`；FeatureArea：`merged_commit / merged_at`。新 Task 均为 NULL。正常 FeatureArea 创建要求明确且唯一的 selected development repository，并记录 Git 当前 dev HEAD（不是过时的 selected_head_sha）及 createdAt。新 split child 同样初始化 baseline。Task 的既有 commit 和 merged checkpoints 不可覆盖；后续合入只更新尚未 MERGED 的 Tasks。

11. **Shared presentation**：Task 提供 `commitStatus / commitSha / devIntegrationStatus / mergedCommit` 和共享中文状态文本；FeatureArea 提供 `commitStatus / mergedCommit / canCommit`，Details 包含时间及完整 SHA。Review/Inspection 不显示代码提交语义。Web、Card、Console、Local MCP 消费相同 Core projection；客户端不执行 Git 或自行推断生命周期。

12. **UI**：Task 没有初次提交按钮；自动提交失败显示“Task 已验收 · 代码提交失败”及 canonical“重试提交”。FeatureArea 满足资格时显示“提交到 <selected branch>”。点击使用 durable requestId，成功后 fresh read。Card 保留重试 requestId 并绑定原 session/FeatureArea/Task，导航后旧按钮失效。

13. **Serialization**：Task finalization 使用 Task command mutex；FeatureArea submit 使用 FeatureArea mutex 加 canonical Git common-directory/development-ref mutex 和 OS file lock。未完成的 repository/ref receipt 还具有唯一索引，跨 response loss/restart 阻止另一 FeatureArea 越过待恢复提交。receipt fence 阻止同 FeatureArea 新 Agent operation、Development Task、workspace 切换及 lifecycle closure；不串行化不同 FeatureArea 的 Development runtime。

14. **Modified files**：以下为最终修改/新增清单；`schema_v42.sql` 被 `schema_v43.sql` 替换。

```text
docs/interfaces.md
docs/task-featurearea-commit-delivery.md
internal/bridgehttp/console_test.go
internal/bridgehttp/development_branch_test.go
internal/bridgehttp/feature_area_actions.go
internal/bridgehttp/ownership_contract_test.go
internal/bridgehttp/product_console_test.go
internal/bridgehttp/server_test.go
internal/bridgehttp/ui/product-app.js
internal/consoleapi/task_action.go
internal/controlapi/errors.go
internal/controlapi/feature_area_presentation.go
internal/controlapi/git_commit.go
internal/featurearea/commit.go
internal/featurearea/featurearea.go
internal/featurearea/service.go
internal/git/client.go
internal/git/commit.go
internal/git/commit_test.go
internal/localmcp/contract_test.go
internal/localmcp/feature_area_continuation_test.go
internal/localmcp/helpers_integration_test.go
internal/mcpserver/decision_resolver.go
internal/mcpserver/dev_bridge_ui.html
internal/mcpserver/development_branch_tools.go
internal/mcpserver/external_contract_test.go
internal/mcpserver/external_workflow_integration_test.go
internal/mcpserver/feature_area_commit.go
internal/mcpserver/git_commit_ui_test.js
internal/mcpserver/reasoning_effort_parity_test.go
internal/storage/sqlite/agent_execution_store_test.go
internal/storage/sqlite/agent_process_migration_test.go
internal/storage/sqlite/commit_migration.go
internal/storage/sqlite/commit_migration_test.go
internal/storage/sqlite/commit_time.go
internal/storage/sqlite/db.go
internal/storage/sqlite/feature_area_lifecycle_store.go
internal/storage/sqlite/feature_area_store.go
internal/storage/sqlite/final_schema_migration.go
internal/storage/sqlite/git_commit_store.go
internal/storage/sqlite/git_commit_store_test.go
internal/storage/sqlite/migrate.go
internal/storage/sqlite/migrate_historical_test.go
internal/storage/sqlite/review_approval_store.go
internal/storage/sqlite/schema_v42.sql
internal/storage/sqlite/schema_v43.sql
internal/storage/sqlite/task_acceptance_store.go
internal/storage/sqlite/task_store.go
internal/task/task.go
internal/workflowpresentation/commit_test.go
internal/workflowpresentation/projection.go
internal/workflowpresentation/task.go
internal/workflowprogression/schema.go
internal/workflowprogression/workflow.go
```

15. **Tests**：全部通过。以下命令均使用 `GOCACHE=/tmp/devbridge-go-cache GOPROXY=off`；HTTP/MCP 测试仅放行本机回环端口。

```sh
go test ./internal/featurearea ./internal/task ./internal/workspace ./internal/worktree ./internal/git ./internal/developmentcycle ./internal/execution ./internal/workflowprogression ./internal/controlapi ./internal/consoleapi ./internal/workflowpresentation ./internal/storage/sqlite ./internal/mcpserver ./internal/localmcp ./internal/bridgehttp -count=1
go test -race ./internal/git ./internal/storage/sqlite -count=1
go vet ./internal/featurearea ./internal/task ./internal/workspace ./internal/worktree ./internal/git ./internal/developmentcycle ./internal/execution ./internal/workflowprogression ./internal/controlapi ./internal/consoleapi ./internal/workflowpresentation ./internal/storage/sqlite ./internal/mcpserver ./internal/localmcp ./internal/bridgehttp
node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js
git diff --check
```

- 完整 Go targeted suite：15 个包通过。
- 完整 core race：Git 通过，SQLite 通过（434.797s）。最后收尾改动后额外跑 Git 全包 race，以及 SQLite commit/recovery/history/concurrent submit/v42→v43 核心 race，均通过。
- Node：251 tests，247 pass，4 skip，0 fail；新增 UI 测试验证 click-only 提交、稳定重试身份、导航 ownership 和 Task 无初次提交按钮。
- `go vet`、`git diff --check`：通过。
- 真实临时 Git＋SQLite 测试覆盖 ACCEPT、REQUEST_CHANGES、REQUEST_REVERIFICATION、REJECT、snapshot drift、Git 成功但 DB 失败、下一 Task 与历史 checkpoint、FF/normal merge/conflict、dirty dev、并行提交、workspace sync、no-op recovery、新 FeatureArea 当前 dev HEAD baseline、Review-only projection，以及 Web/Card/Console/Local MCP parity。

最终完整 Go suite 输出：

```text
ok  	github.com/lazyun/devbridge/internal/featurearea	1.033s
ok  	github.com/lazyun/devbridge/internal/task	2.814s
ok  	github.com/lazyun/devbridge/internal/workspace	1.521s
ok  	github.com/lazyun/devbridge/internal/worktree	4.002s
ok  	github.com/lazyun/devbridge/internal/git	12.703s
ok  	github.com/lazyun/devbridge/internal/developmentcycle	2.007s
ok  	github.com/lazyun/devbridge/internal/execution	11.753s
ok  	github.com/lazyun/devbridge/internal/workflowprogression	2.341s
ok  	github.com/lazyun/devbridge/internal/controlapi	11.720s
ok  	github.com/lazyun/devbridge/internal/consoleapi	2.968s
ok  	github.com/lazyun/devbridge/internal/workflowpresentation	3.148s
ok  	github.com/lazyun/devbridge/internal/storage/sqlite	46.096s
ok  	github.com/lazyun/devbridge/internal/mcpserver	19.060s
ok  	github.com/lazyun/devbridge/internal/localmcp	53.031s
ok  	github.com/lazyun/devbridge/internal/bridgehttp	18.912s
```

16. **Final Git state**：HEAD 和 branch 保持上述起始值；源码修改与新增文件均未 stage、未 commit。`git diff --check` 通过。全部修改限定于本次实现、schema、文档和测试 fixture。

```text
 M docs/interfaces.md
 M internal/bridgehttp/console_test.go
 M internal/bridgehttp/development_branch_test.go
 M internal/bridgehttp/feature_area_actions.go
 M internal/bridgehttp/ownership_contract_test.go
 M internal/bridgehttp/product_console_test.go
 M internal/bridgehttp/server_test.go
 M internal/bridgehttp/ui/product-app.js
 M internal/consoleapi/task_action.go
 M internal/controlapi/errors.go
 M internal/controlapi/feature_area_presentation.go
 M internal/featurearea/featurearea.go
 M internal/featurearea/service.go
 M internal/git/client.go
 M internal/localmcp/contract_test.go
 M internal/localmcp/feature_area_continuation_test.go
 M internal/localmcp/helpers_integration_test.go
 M internal/mcpserver/decision_resolver.go
 M internal/mcpserver/dev_bridge_ui.html
 M internal/mcpserver/development_branch_tools.go
 M internal/mcpserver/external_contract_test.go
 M internal/mcpserver/external_workflow_integration_test.go
 M internal/mcpserver/reasoning_effort_parity_test.go
 M internal/storage/sqlite/agent_execution_store_test.go
 M internal/storage/sqlite/agent_process_migration_test.go
 M internal/storage/sqlite/db.go
 M internal/storage/sqlite/feature_area_lifecycle_store.go
 M internal/storage/sqlite/feature_area_store.go
 M internal/storage/sqlite/final_schema_migration.go
 M internal/storage/sqlite/migrate.go
 M internal/storage/sqlite/migrate_historical_test.go
 M internal/storage/sqlite/review_approval_store.go
 D internal/storage/sqlite/schema_v42.sql
 M internal/storage/sqlite/task_acceptance_store.go
 M internal/storage/sqlite/task_store.go
 M internal/task/task.go
 M internal/workflowpresentation/projection.go
 M internal/workflowpresentation/task.go
 M internal/workflowprogression/schema.go
 M internal/workflowprogression/workflow.go
?? docs/task-featurearea-commit-delivery.md
?? internal/controlapi/git_commit.go
?? internal/featurearea/commit.go
?? internal/git/commit.go
?? internal/git/commit_test.go
?? internal/mcpserver/feature_area_commit.go
?? internal/mcpserver/git_commit_ui_test.js
?? internal/storage/sqlite/commit_migration.go
?? internal/storage/sqlite/commit_migration_test.go
?? internal/storage/sqlite/commit_time.go
?? internal/storage/sqlite/git_commit_store.go
?? internal/storage/sqlite/git_commit_store_test.go
?? internal/storage/sqlite/schema_v43.sql
?? internal/workflowpresentation/commit_test.go
```

17. **SCOPE_CONFLICT**：单 repository 的本次目标没有未解决范围冲突。现有 FeatureArea 创建参数没有 repository 选择字段；如果一个 Project 同时选择多个 development repositories，新建 FeatureArea 返回明确的 `SCOPE_CONFLICT`，不任意选取其中一个 SHA。未选择 development branch 则返回 `DEVELOPMENT_BRANCH_NOT_SELECTED`。

18. **交付边界**：未 push；未提交本次源码、未 merge main、未 tag/release、未清理其他 worktree。没有访问 live SQLite、外部网络或真实 Codex Turn；验证均使用临时数据、临时 Git repository、fake backend 和本机回环服务。
