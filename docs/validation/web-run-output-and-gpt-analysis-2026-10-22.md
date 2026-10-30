# 网页执行输出与 GPT 分析展示（2026-10-22）

## 问题与修复

原 Console 只投影 `analysis_state`，未返回 `run_analyses` 中已提交的 GPT 报告及回执。运行详情主要显示 Git 证据或单行原始输出预览；运行终态又会停止读取，晚于 Codex 完成入库的 GPT 分析无法自动出现。

- Console 的准确 Run 详情和完整审查结果现在分别返回 Codex 原文、GPT 原始报告和提交回执。读取走 Core 的本地操作员项目边界，不借用或创建分析作者的 AI Session；事务读取保持同一数据库快照，不修改业务状态。
- 任务概览新增审查、实施和验证结果入口。Run 的“结果与 GPT 分析”页和运行会话的同名标签提供多行正文、检查/验收项、实施与验证方案及完整原始报告。
- GPT 报告与 Codex 原文独立展示；未提交、读取失败和原文截断分别提示。文本按纯文本转义，不执行 HTML，不将运行成功解释为验收通过。
- 运行终态而分析待提交时，仅进行只读刷新；报告到达后停止轮询。切换 Run 或隐藏页面不让旧响应覆盖当前内容。

## 验证

- `TestRecordedRunAnalysisConsolePreservesOriginalSourcesAndScope`：真实 Codex 协议夹具、GPT 提交、SQLite 与 Console 读取链路通过；报告及回执逐字一致，原生输出保持独立，AI Session 不能当本地操作员，跨项目/任务拒绝，读取前后全部表快照不变。日志 `build/result-visibility-source-contract.log`。
- Web JavaScript 142 项通过，包括正文可读、原文保留、HTML 转义、待提交/失败/截断状态、准确 Run 入口、终态只读刷新及迟到响应隔离。日志 `build/result-visibility-node.log`。
- Web Go 全套及 Console Go 全套通过（86.640 秒 / 2.467 秒）。SQLite 首次全套在 180 秒总时限耗尽时停止，单项并非断言失败；另以 600 秒时限重跑。日志 `build/result-visibility-go.log`、`build/result-visibility-sqlite.log`。
- 相关 `go vet` 与差异空白检查通过。三个 `.26` 候选二进制已构建于 `build/full-flow-update-20261022-26/`。
- 用原任务已入库的真实审查报告实测生产渲染组件，确认 GPT 正文、完整报告和 Codex 原文可分别阅读。只读预览截图 `build/result-visibility-preview.png`。

## 本机部署与实际回读

用户明确选择“立即重启更新，允许中断当前验证”后，经 Desktop 停止受管进程，确认 WAL 已回收，备份数据库（0600）和三个旧二进制，原子更新到 `.26`。三个 readyz 均 200，真实连接器诊断返回 `0.1.0-dev.full-flow-20261022.26+gd3969cf`。

真实接口和浏览器已核对：

- 原审查 Run 原文 7260 字符，GPT 分析 `RECORDED`、正文和原始回执可读，页面不再只有“已入库”提示。
- 实施 Run 原文 2885 字符，可阅读全文；GPT 分析为 `OPTIONAL`，尚无正式报告提交记录。网页明确提示这一事实，不把对话正文推定为已入库。
- 原验证 Run 在授权重启后收口为 `INFRASTRUCTURE_FAILED`，失败码 `bridge_shutdown_interrupted`；Task 当前需要恢复，未自动重试、重新审批或伪造验证通过。
- 其余 9 条历史 Run、全部已有 GPT 分析、17 个确认 Intent 和 18 条确认提交记录的前后摘要完全一致。
- 真实任务详情、运行会话的“结果与 GPT 分析”标签均已实测。生产截图 `build/result-visibility-live.png`；预览截图不代替生产验收。

SQLite 全套复查曾因磁盘空间不足出现 `no space left on device` / `disk I/O error`。仅清理本次专用 Go 构建缓存释放约 2GB，未清理用户文件或任务数据，随后重跑全套并通过（243.064 秒）。对应日志 `build/result-visibility-sqlite-space-retry.log`。

本机备份、前后摘要与二进制摘要位于 `build/full-flow-update-20261022-26/`，不提交这些本机构建材料。
