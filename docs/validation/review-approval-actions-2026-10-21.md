# 等待审查审批操作入口（2026-10-21）

## 修正

Core 已返回待审批 Decision 与 `APPROVE`、`REVISE`、TASK 取消资格，但工作区卡片只显示事实文字，没有入口。本轮在当前任务等待审查审批时增加“审批确认”“要求修改方案”“取消任务”。`REVIEW · SUCCEEDED` 是审查 Run 成功，不代表用户批准或 Task 完成。

入口使用当前 Core 资格，固定 Session、Project、FeatureArea、Task、Review、定义修订与 Decision。历史只读、失效或已处理决策、阶段变化、读取不完整和目标冲突时不显示或拒绝旧按钮。收起卡片和紧凑进度也提供入口。

- 审批确认：显式点击发送准备请求，要求 GPT 读原报告，原样保留实施与验证方案，执行 `execution_prepare(APPROVE)`，再 `confirmation_show`。最终审批由正式确认卡片提交；不调用 `confirmation_commit` 或自动启动实施。准入失败保留真实错误，不降低检查或推定豁免。
- 要求修改方案：先在对话中收集具体反馈，再走准确 Review 的修订确认，不编造反馈。
- 取消任务：先展示本地取消确认，只有点击“确认取消任务”才发送明确取消请求。GPT 必须重新核对 TASK 取消资格，使用准确 Task owner，不取消已完成的 Review/Run；未知结果查原回执，不换 requestId 重发，ACK 不冒充终态。“暂不取消”不发送消息。

工作区仍不直接调用业务写工具；通过明确的用户点击进入原有正式协议。发送成功只表示请求已接受，业务状态以 Core 回执为准。超时不自动重发，提供手动指令。挂载和轮询无业务提交。

资源升级 `ui://devbridge/card/read-only-review-actions-v6`，保留 v5 和所有旧 URI。

## 验证

| 检查 | 结果 |
| --- | --- |
| 聚焦审批与取消入口用例 | 8 PASS |
| 全部工作区卡片 JS 用例 | 153 PASS，3 SKIP，0 FAIL |
| Go MCP 集成测试 | PASS，43.611s |
| `go build -p 1 ./cmd/...` | PASS |
| 真实宿主入口与取消预览 | PASS，三个入口可见；取消确认可展开并用“暂不取消”退出 |

真实任务不用于执行审批或最终取消。仅验证入口可见和无业务变更的取消预览，正式动作路径由隔离用例验证。当前原始报告仍有必需 CUSTOM 检查与执行准入不匹配的问题，此轮不修改正式报告。

部署版本 `0.1.0-dev.review-actions-20261021.8+gd3969cf`，真实 Tunnel diagnostics 为 `ready=true`。主脚本 SHA-256：`6f7f5389dfa01529f823a6aa23f553653fc3b4f0ae7ceb213f0f09c5c0fe77bf`。

真实 Work 对话中返回新卡片；首次组件加载失败，使用宿主“重试”后恢复。侧栏显示“审批确认”“要求修改方案”“取消任务”。仅展开取消预览并点击“暂不取消”，没有发送最终取消请求，没有审批或启动实施。真实提交审批/取消未执行，不能将入口验收描述为业务提交成功。

私人截图位于忽略的 `build/review-actions-evidence/approval-actions.png` 和 `build/review-actions-evidence/cancel-preview.png`，不纳入公开仓库。
