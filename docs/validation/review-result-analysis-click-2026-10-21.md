# 审查完成后点击 GPT 分析（2026-10-21）

## 修正的行为

用户需要的是审查结束后由 GPT 分析实际报告。前一轮的“询问 ChatGPT”只是查询任务进度，未满足该用途。本轮将其替换为 **让 GPT 分析审查结果**，保留已有消息桥接、超时和只读边界。

- 仅在准确 Task 下的 Review 已结束且结果可读时显示；未结束、无结果、身份或归属不匹配时不显示。
- 内联展开、收起、紧凑进度与具体历史 Review 操作区均可请求分析。消息固定完整 Review ID，不换成最新审查。
- 使用既有 `Analysis review result <reviewId>` 精确分析契约，要求 `operation_get` 读取 `RESULT`；引用正式报告时读取同一 Review 的 `review_result_get`，必要时读取该 Review 的证据。
- 分析内容包括结论与依据、拟修改文件和范围、风险和遗漏、验证充分性与可执行性、未证实假设、批准建议及前置问题。区分报告主张与 Core 观测事实。失败或取消仅分析实际可读部分并说明局限。
- 完成分析后等待用户另行决定，不批准、不启动、不提交新报告、不改文件或订阅。GPT 自己提交的报告不会自动再分析，用户显式点击时可分析。
- 同一 Review 的多个按钮共享一次发送记录，新 Review 可以独立请求。过期 DOM 按钮、迟到响应和历史导航不能改变目标。消息超时或失败不重发，提供手动分析指令。“复制分析指令”与点击路径使用同一完整提示。

资源升级为 `ui://devbridge/card/read-only-review-analysis-v5`，保留 v4 和全部此前 URI。普通对话仍可发送同样指令。

## 验证

| 检查 | 结果 |
| --- | --- |
| 聚焦消息与 Review 分析用例 | 13 PASS |
| `node --test internal/mcpserver/dev_bridge*test.js` | 145 PASS，3 SKIP，0 FAIL |
| `go test -p 1 ./internal/mcpserver` | PASS，42.281s |
| `go build -p 1 ./cmd/...` | PASS |
| 真实 Work 对话点击并分析报告 | PASS |

首次并行构建遇到本机磁盘空间不足；顺序重跑的最终结果记录于上表。实际 PiP 显示模式的验收边界仍以[PiP 验证](task-progress-pip-2026-10-21.md)为准。

服务版本：`0.1.0-dev.review-analysis-20261021.7+gd3969cf`，实际 Tunnel diagnostics 为 `ready=true`。

主脚本 SHA-256：`ccf4ac7b21dbbabb80375a5eb253e4b2279bf13e21c8cc680f6f89b956b0ddce`。

## 真实宿主结果

在原 ChatGPT Work / Cloud 对话返回新卡片后，宿主首次组件加载失败；使用“重试”恢复，没有变更安全设置。紧凑进度显示“让 GPT 分析审查结果”。实际点击后宿主接受消息并返回内联，准确 Review 的按钮同步变为“已请求分析”。

GPT 完成回复，列出一致的 Task、Review、正式报告和基线身份，并分析两文件修改范围、标准库识别及精确 flow 白名单风险、四文件证据覆盖局限、没有运行验证的缺口，以及必需 CUSTOM 检查无法通过当前准入的问题。它明确区分报告结论与 Core 观测，给出先解决验证方案、再决定批准的建议。此次回复已超出通用任务状态查询，符合实际报告分析用途。

验收后 Task 仍为 `REVIEW_APPROVAL_PENDING`、无当前 Execution，Run 计数未变，Events 订阅仍 `ACTIVE`，SQLite quick check 为 `ok`。包含私人对话的截图保存在被忽略的 `build/review-analysis-evidence/analysis-button.png` 和 `build/review-analysis-evidence/gpt-review-analysis.png`，不进入公开仓库。
