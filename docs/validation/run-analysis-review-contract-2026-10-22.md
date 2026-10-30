# GPT 审查分析目标契约修复（2026-10-22）

已部署 `0.1.0-dev.full-flow-20261022.23+gd3969cf`，数据库 schema 保持 130。

## 原因与修复

“重构 Progressive Discovery 为节点驱动流程”的 Codex 审查已完成，使用冻结的单仓库标量 Review：仓库与基线明确，但 `repositoryTargets` 为空，没有成员身份或目标别名。原 `run_analysis_get` 返回同时允许 v1/v2 的通用报告格式，未明确此时必须使用 v1 和空别名，导致 GPT 误以为缺少仓库身份，无法安全提交。

分析描述现在从原 Review 冻结目标生成本次专属 `reportSchema`，不改变原 Review、权限、解析校验或 `sourceHash`：

- 单仓库标量 Review：`version=1`、`repositoryTargets=[]`、`primaryTargetAlias=""`；每项验证的 `targetAlias=""`、`targetAliases=[]`、`workingDirectory=""`，命令仍在原仓库根执行。实施范围使用 `PATHS` 或 `REPOSITORY`，不虚构 `target-1` 或成员身份。
- Workset Review：`version=2`，明确逐项冻结别名与仓库、成员、版本、基线的映射，以及原主目标。格式只允许本次别名；实施范围使用 `REPOSITORY_TARGETS`。模型仍只提出写范围，不能提交或替换 Core 身份。
- 格式在每次读取时独立生成，不修改共享协议、原生 Codex 输出协议或历史报告。MCP 工具名称与输入结构未改变。

## 验证

- `internal/review`、`internal/execution`、`internal/mcpserver` 全包回归通过，见本机 `build/analysis-contract-regression.log`。
- 新测试覆盖单仓库与 Workset 格式、有效报告解析、错误协议与未知别名拒绝、冻结源和全局格式不被修改，见 `build/analysis-contract-focused.log`。
- 临时数据库真实流程通过：自然输出完成后，按返回的单仓库格式提交独立 GPT 报告；虚构别名被拒绝，随后正确提交成功、可精确重放并生成待审批决定，不创建实施。原始输出保持不变。见 `build/analysis-contract-storage.log`。
- 相关五个包 `go vet` 与相关改动 `git diff --check` 通过，见 `build/analysis-contract-vet.log`。

## 本机更新与原任务核对

更新前确认 0 条活动 Run，经 Desktop 停止受管服务，备份正式数据库（0600）及三个旧二进制，再原子替换并启动。Desktop 显示三个服务均 `MANAGED / RUNNING / ready`，隧道 ready；三个 `/readyz` 返回 200。

通过真实 ChatGPT 连接器重新读取原 Run，确认 `reportSchema` 仅允许 v1、空目标数组与空别名，且 `sourceHash` 与更新前一致。8 条历史 Run 的状态、Thread/Turn 和原始结果摘要一致，分析记录数仍为 0，schema 仍为 130。

原 Session、FeatureArea、Task、Review、Run 保留；状态仍为 `REQUIRED`，等待原对话重新读取并提交 GPT 分析。本次没有替用户编写或提交该业务审查报告，没有批准或启动实施，也未重跑审查。

本机备份、版本与前后状态摘要：`build/full-flow-update-20261022-23/`。这些构建材料被忽略，不随本文提交。
