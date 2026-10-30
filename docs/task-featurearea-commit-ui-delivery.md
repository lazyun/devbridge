# Task 自动提交与 FeatureArea 提交 UI 交付记录

> Historical implementation evidence. [GPT surface consolidation V1](architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

1. **CORE_COMMIT**：`f9baab2454b10ce458978b73d437d943e826c162`，提交信息 `feat: add task auto-commit and feature area submission`。提交前 HEAD 为 `0b4d55b3e8f56ba4e805098990097048f0892bb7`；54 个状态条目与此前交付清单一致，暂存区为空，diff 检查通过。提交保存 53 个文件（schema 文件重命名合并计数），之后工作区为空。
2. **UI 起始 HEAD**：同 CORE_COMMIT；当前仍在 `dev_20260908_1913`。UI 修改保持未提交。
3. **Web 信息结构**：Task 原有 Review → Approved → Implementation → Verification → Done 单轨保持不变；紧邻进度条显示独立 Code/Dev 块。FeatureArea 生命周期、提交状态分开；Area 内 Task 列表和 Tasks 页采用 Task / Status / Code / Dev，窄屏保留这些字段。
4. **Card 信息结构**：沿用 Project / Branch、FeatureArea / Task 选择器、单一 Task 进度轨、Code/Dev、Codex activity 和现有结果/详情。FeatureArea 提交只在区域概览操作；Task 页只显示与该 Task 对应的重试，不添加初次手动提交入口。
5. **Task 体验**：未验收显示“代码：未提交”；持久化 pending receipt 且没有错误时显示“代码：正在提交…”；checkpoint 到达显示短 SHA；未合入显示“Dev：等待功能领域提交”；合入后显示短 merge SHA。只有验收成功、未提交且存在持久化错误时显示“Task 已验收 · 代码提交失败”和人类可读说明。
6. **FeatureArea 体验**：始终显示提交状态；按 Core canCommit 显示目标分支按钮。执行期间按钮禁用并显示“正在提交…”。禁用原因由共享展示层解释，包括等待 Task、自动提交、验证验收、工作区改动和恢复。成功后定向重读区域与所属 Task；导航后的旧响应不能替换当前抽屉。
7. **重试与恢复**：Task 仍调用 canonical retry；共享展示要求 Core capability 与持久化错误同时满足。FeatureArea pending 恢复优先复用 Core requestId，响应不确定时保留本地 requestId。已有 pending 错误显示“提交状态需要恢复”；详情保留 request 与完整错误。没有增加新的写命令。
8. **冲突体验**：显示“无法提交到 dev：存在代码冲突”，保留未提交和原生命周期；错误全文在 Details。只在读取 Core 已完成冲突 receipt 后清除旧命令身份，后续显式重试仍须 Core canCommit；pending recovery 不生成新身份。不自动修复冲突。
9. **共享事实来源**：workflowpresentation 统一生成 CommitView、Task commitActivity/summary/errorSummary。Control API、Console DTO、Web、Card 和 RenderText 消费同一投影。新增 SQLite 文件仅 SELECT 已有 v43 receipt，并读取当前 Git HEAD；未修改 schema、事务、Git 写入、锁、验收、verification 或 workspace 生命周期。Task 列表的 checkpoint 字段直接来自同一持久化 Task。
10. **同 Card 与刷新**：mutation 后使用 task_presentation_get / feature_area_presentation_get 替换当前投影；没有调用 card_show 或新建 Card。自动刷新每 5 秒且仅针对已打开对象；Task 的业务 terminal 不会提前停止尚在 pending 的代码提交观察。完成、失败后停止，隐藏页暂停，导航隔离迟到响应。区域轮询期间不反复加载整个项目；进入区域和提交终态按所属 Task 定向读取。
11. **文件清单**：见下方完整 Git 状态。Details 包含 Task 完整 commit/merge SHA 与时间、receipt 错误；Area 包含 merge SHA/时间、工作区 HEAD、dev HEAD、source/expected HEAD、request 和恢复错误。Git HEAD 无法读取时留空，不伪造值。
12. **Node**：`node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js`，263 项，259 通过、4 项原有跳过、0 失败。覆盖 product_ui、product_work_refresh、dev_bridge_ui，以及新增 Web/Card 提交专项测试；Card 测试被已有聚合测试重复引入，以上为 runner 的实际总数。
13. **Go / vet**：下方保留最终包结果。另加只读存储测试，验证现有错误 receipt 的展示、HEAD 未推进、跨项目隔离。最终文字展示小改后补跑 workflowpresentation。受影响包 go vet 全部通过。首次沙箱执行因 127.0.0.1 临时端口绑定受限失败，随后授权使用本机测试端口复验通过；所有测试均使用临时 DB / Git、fake backend，GOPROXY=off。
14. **diff 检查**：`git diff --check` 通过；最终未提交修改仅为本轮 UI / 展示 / 测试 / 本文档。Core 写逻辑保持基线版本。
15. **Git 状态**：HEAD 保持 CORE_COMMIT，暂存区为空；见下方完整状态。
16. **SCOPE_CONFLICT**：未发现需要修改冻结 Core 的缺陷。当前展示把“未完成且无错误的 receipt”解释为正在提交；既有 receipt 没有独立 Git worker 心跳，因此该状态描述持久化进度，不声称能证明进程实时存活。恢复操作仍完全遵守 Core 能力。
17. **发布操作**：没有 push、合并 main、tag、release，没有修改其他 worktree，没有真实 Codex、外部网络或用户运行中数据库操作。

## 验证覆盖

| 要求 | 验证 |
|---|---|
| A–E Task 未验收、进行中、成功、失败、已合入 | workflowpresentation/git_test.go 的表驱动测试；Web/ Card 定向刷新测试 |
| F–G 区域可提交、禁用原因 | 共享展示与 Web 提交面板测试 |
| H–I 防双击、成功刷新相关 Task | product_git_commit_test.js；git_commit_ui_test.js |
| J–K 冲突与恢复原身份 | 共享展示、Web、Card 专项测试 |
| L 共享投影与文本 / Console | RenderText 同事实断言；Control / Console / MCP / Local MCP / HTTP 回归 |
| M 同 Card 定向刷新 | dev_bridge_ui_test.js 与 git_commit_ui_test.js |
| N 主视图短 SHA / Details 完整事实 | 共享展示与 Task 列表断言 |

## Go 最终输出

```text
ok  	github.com/lazyun/devbridge/internal/workflowpresentation	0.317s
ok  	github.com/lazyun/devbridge/internal/controlapi	3.813s
ok  	github.com/lazyun/devbridge/internal/consoleapi	1.198s
ok  	github.com/lazyun/devbridge/internal/mcpserver	13.853s
ok  	github.com/lazyun/devbridge/internal/bridgehttp	11.894s
ok  	github.com/lazyun/devbridge/internal/localmcp	49.519s
ok  	github.com/lazyun/devbridge/internal/storage/sqlite	1.165s
ok  	github.com/lazyun/devbridge/internal/workflowpresentation	0.613s
```

## 最终工作区状态

```text
 M internal/bridgehttp/product_ui_test.js
 M internal/bridgehttp/product_work_refresh_test.js
 M internal/bridgehttp/ui/product-app.js
 M internal/bridgehttp/ui/product-work-refresh.js
 M internal/bridgehttp/ui/product.css
 M internal/consoleapi/product.go
 M internal/controlapi/git_commit.go
 M internal/mcpserver/dev_bridge_ui.html
 M internal/mcpserver/dev_bridge_ui_test.js
 M internal/mcpserver/git_commit_ui_test.js
 M internal/workflowpresentation/commit_test.go
 M internal/workflowpresentation/projection.go
 M internal/workflowpresentation/task.go
 M internal/workflowpresentation/text.go
?? docs/task-featurearea-commit-ui-delivery.md
?? internal/bridgehttp/product_git_commit_test.js
?? internal/featurearea/commit_facts.go
?? internal/storage/sqlite/commit_presentation.go
?? internal/storage/sqlite/commit_presentation_test.go
?? internal/workflowpresentation/git.go
?? internal/workflowpresentation/git_test.go
```
