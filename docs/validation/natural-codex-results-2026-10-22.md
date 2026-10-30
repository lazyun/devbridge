# Codex 自然输出与 GPT 独立分析验收（2026-10-22）

已部署 `0.1.0-dev.full-flow-20261022.22+gd3969cf`，数据库 schema 130。

## 当前行为

- 新 Codex Run 使用 `NATURAL_V1`，不发送最终回复的 `outputSchema`。审查、实施、验证、Inspection 和 Recovery 诊断均保留原始输出；正常结束只表示运行完成。
- Core 单独保存不可变命令记录、退出码和验证检查观测。验证观测在运行结束时采集，GPT 分析不重新执行命令，也不依赖之后的工作区状态来重建这些观测。
- GPT 通过 `run_analysis_get` 读取原始输出、冻结方案、证据和本次分析格式，再使用准确 `sourceHash` 与稳定 `requestId` 调用 `run_analysis_submit`。格式约束属于 GPT 的显式提交接口。错误提交可修正，无需重跑 Codex。
- 审查分析提交后进入原审批流程；验证分析与报告入库后进入用户验收。实施摘要可选。GPT 声称通过不能覆盖真实失败退出码。报告、运行成功、用户批准和验收仍为不同事实。
- 原权限边界、冻结作用域、身份绑定、取消和 UNKNOWN 防重发约束保留。Recovery 原任务权属不会因诊断运行成功或 GPT 分析而释放，仍须原恢复决议。
- WebUI / Card 显示“Codex 已完成，等待 GPT 分析”。卡片可请求 GPT 分析，不直接提交分析或用户审批。新资源为 `natural-result-analysis-v9`，保留旧 URI 兼容入口。
- 已派发和已结束的历史 Run 保持原协议，不补写自然输出标记，不重解释旧报告。

## 验证

- 双仓库生产 Facade 隔离流程通过：自然审查输出 → 独立 GPT 分析 → 用户审批 → 实施 → 验证 → GPT 分析 → 用户验收 → 提交与逐仓合入。仅临时测试仓库发生 Git 操作，正式业务任务未启动或修改。见 `build/natural-flow-final.log`。
- 单仓库与双仓库 Direct File 实施后的 Codex 验证通过；缺少目标仓库的命令证据不能被 GPT 的通过声明替代。见 `build/natural-direct-verification.log`。
- 本机 Codex 0.159.2 的 Inspection 自然语言结果、独立分析及 Recovery 等待分析流程通过；恢复分析没有重发 UNKNOWN 回答或自动解决源任务。见 `build/natural-inspection-native.log`、`build/natural-recovery-native.log`。
- 隔离安装的 0.163.0 通过 Inspection 与审查原回合 Core 重启衔接。见 `build/natural-native-0163.log`、`build/natural-review-restart-0163.log`（两个审查子测试实际执行，保持原 App Server / Thread / Turn）。未更换全局 Codex。
- SQLite 定向回归覆盖自然输出不提前生成审批、准确源校验、调用者隔离、提交幂等、源任务权属及历史 materialization 兼容；执行器测试覆盖自然文本不能声明 Core 范围冲突、真实失败证据不能被报告覆盖。见 `build/natural-storage-final.log`、`build/natural-final-guards.log`、`build/natural-execution-focused.log`。
- GPT MCP 全套通过；Local MCP 发现与目录契约通过；新分析工具实际 SDK wire 读写通过，缺少源校验值被拒绝。见 `build/natural-mcp-final.log`、`build/natural-local-catalog.log`、`build/natural-mcp-wire.log`。
- Web/Card 测试 404 通过、4 跳过、0 失败；新增待分析卡片测试通过。相关包 `go vet`、相关改动 `git diff --check` 与三个二进制构建通过。见 `build/natural-ui-tests.log`、`build/natural-card-test.log`、`build/natural-vet.log`。

首次验收时，execution 的部分旧测试夹具在进入运行前被 `STALE_REVIEW_APPROVAL` / `UNOBSERVABLE_REQUIRED_CHECK` 拒绝，Local MCP 全套批次也未完成，因此当时未将完整 Go 回归作为全绿证据。后续已修复旧夹具，主模块默认 Go 回归分批复验通过；具体覆盖与默认跳过项见[旧夹具修复记录](legacy-fixtures-2026-10-22.md)。

## 部署核对

先在数据库副本预演 129 → 130，迁移前后 7 条 Run 的状态、Thread/Turn 与原始结果摘要相同，新协议记录数为 0。正式更新前活动 Run 为 0，通过 Desktop 停止受管服务，备份 v129 数据库及三个旧二进制，再原子替换并启动。

Desktop 显示 Core、GPT MCP、Local MCP 均 MANAGED / RUNNING / ready，隧道 ready；三个回环 `/readyz` 均 HTTP 200。正式数据库正常迁移至 130，历史摘要仍相同，活动 Run 与新协议 Run 均为 0。Desktop 已实际读取原任务的历史会话、命令、退出码及旧验证报告。

生产协议发现确认 GPT MCP 发布 65 个工具（含 1 个 app-private），Local MCP 发布 60 个工具，均包含 `run_analysis_get` 与 `run_analysis_submit`。未创建业务 AI 会话、执行任务、提交或推送当前仓库。

构建、0600 数据库备份与部署摘要：`build/full-flow-update-20261022-22/`。截图：`build/natural-result-services-deployed.png`。
