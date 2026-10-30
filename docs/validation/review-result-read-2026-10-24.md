# 审查报告读取协议兼容修复

状态：2026-10-25 已部署 `0.1.0-dev.full-flow-20261025.70+gc4ae1d3`，面板 v39、确认单 v11、schema 132。此修改只调整报告读取，不迁移数据库，不补写报告，不改变审批或运行权限。

## 已复现的问题

一条 `CODING_AGENT`、`NATURAL_V1` 的历史 Review 已完成，其 GPT 正式分析已记录在 `run_analyses`，且 canonical `review_result_json` 与 `review_approval_decisions.result_text` 一致。

`operation_get` 错误地对 Codex 原始中文自然语言调用 `review.ParseResult`，导致 `REVIEW_SUMMARY` 和 `REVIEW_PROPOSAL` 返回 JSON 解析错误。`review_result_get` 则只查询 Upper Review 的 `review_reports`，对实际存在的 Coding Review 分析返回 `NOT_FOUND`。

## 修复后的读取合同

两个入口共用准确 Review/Run 身份与结果协议的读取逻辑，不查询“最新审查”替代历史对象：

| 来源 | `review_result_get` 结果 |
| --- | --- |
| GPT 直接审查，`UPPER_LLM` | 保持原 `report`、`receipt` 格式与原作者会话权限限制；标明 `source=UPPER_REVIEW_REPORT` |
| Coding Review，`NATURAL_V1`，正式分析已记录 | `source=GPT_RUN_ANALYSIS`，`state=RECORDED`，`analysis.report` 返回原提交 JSON，`analysis.receipt` 返回原回执，`proposal` 返回原持久化 canonical 结果 |
| Coding Review，尚无正式分析 | 返回实际分析状态；不把原始输出当报告，不伪造提案或成功回执 |
| 历史结构化 Review | 沿原 JSON 解析合同读取，`source=VALIDATED_REVIEW_RESULT` |

`operation_get(include=["RESULT"])` 在 `reviewResult` 返回上述同一结果；兼容字段 `reviewProposal` 继续保留，并使用 `GPT_RUN_ANALYSIS:<runId>` 等明确来源。摘要取正式结果，原生文本仍留在独立 `AGENT_CLAIM` 中。尚待分析时摘要状态为 `WAITING_ANALYSIS`，不产生原始文本 JSON 解析错误。

`run_analysis_get` 增加只读 `reviewResult` 与 `recordedSourceHash`，分别来自原 `review_result_json` 和提交时的 `source_hash`。当前 `sourceHash` 的计算和提交检查不变，历史 hash 不被重新计算后覆盖。

指定 `requestId` 时只返回准确匹配的原提交回执；错误请求拒绝，不借用其他回执。所有读取保留 Project、Task、FeatureArea、Review、Run、purpose、baseline 校验和原有会话边界；同项目且合法绑定的其他会话可以读取原 Coding 分析，跨项目拒绝。没有新的提交、授权或执行入口。

MCP 工具显式声明 JSON RawMessage 的输出 schema，防止 SDK 把原报告对象推断成字节数组而拒绝合法响应。

## 验证

日志位于 `build/review-result-read-20261024/`。定向验证覆盖原生中文输出与正式结果分离、缺失分析、源读取失败、错误身份及 baseline、错误回执、旧结构化结果、Upper 报告、真实 MCP tools/call 输出、同项目跨会话读取、跨项目拒绝、Task 正式通过审批后读取原报告，并检查读取不增加报告记录。

| 验证 | 结果 | 日志 |
| --- | --- | --- |
| 完整 MCP 测试，含真实 tools/call 输出 schema | PASS，54.645s | `mcp-domain-tests.log` |
| 完整 Review 领域测试 | PASS，0.498s | `mcp-domain-tests.log` |
| 完整 SQLite 测试，含分析写入/读取、权限与历史结果 | PASS，291.712s | `sqlite-tests.log` |
| Core 已记录分析读取、Upper v1、修订与准确审批结果定向回归 | PASS，12.438s | `core-tests.log` |
| 历史读取及新读取入口定向测试 | PASS | `targeted-tests.log`、`history-tests.log` |
| 受影响模块 go vet | PASS | `vet.log` |
| git diff --check | PASS | `diff-check.log` |

原生产案例仅通过只读 SQLite 连接复核：canonical 结果仍与原审批记录逐字节一致，原结果哈希和提交 payload hash 未变化，`review_reports` 未补造任何记录；证据在 `original-case-readonly.json`。未用旧部署的 MCP 结果冒充修复后的线上验证，也未执行完整 Core 测试包。

## 部署回读

部署记录位于 `build/full-flow-update-20261025-70/`。构建使用 `c4ae1d3` 加本次通过验证的准确源码，未包含工作区中另外的 Desktop 与 `product.css` 改动。三个候选服务先通过空数据目录启动检查，随后通过 Desktop 停止和启动托管服务。没有活动 Run 或领域执行占用，没有启动工程任务，没有创建回滚备份。

Core、GPT MCP、本地 MCP 和 OpenAI 隧道均就绪，两个 MCP 初始化版本均为 `.70`，面板 v39。重启和回读前后核对 32 张业务表，行数与内容哈希均不变；SQLite 完整性为 `ok`，schema 仍为 132。

对 `review_1cf79e4111e31fd3192903e95944f695`、`run_ac4fe956530d7266b99c3eb01a353c84` 的两个真实连接器读取均成功。`operation_get` 的 `REVIEW_SUMMARY`、`REVIEW_PROPOSAL` 无读取错误；`review_result_get` 返回 `GPT_RUN_ANALYSIS / RECORDED`。两者的正式结果对象完全一致，disposition 为 `APPROVAL_READY`。回读的原报告、回执、canonical 提案和记录时 source hash 与原 SQLite 记录一致，canonical 结果及 payload hash 未变化。证据为 `original-case-readback.json`、`verification.json` 和 `deployment.json`。
