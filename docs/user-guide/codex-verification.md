# 验证统一由 Codex 执行

实施方式和验证执行者分开：

| 实施方式 | 修改文件 | 实际执行验证 |
| --- | --- | --- |
| GPT 文件接口（DIRECT_FILE） | 发起对话中的 GPT | 独立的 Codex VERIFICATION Run |
| Codex（CODING_AGENT） | Codex IMPLEMENTATION Run | Codex VERIFICATION Run |

GPT 可以分析审查报告、解释命令结果、判断是否建议验收。文件阅读和模型陈述不会替代必需检查的实际执行证据。

## GPT 文件实施

1. 审查方案明确文件修改范围与 `RUN` 验证规格。每项必需命令检查包含准确命令；多仓库检查还必须包含仓库、工作目录和覆盖范围，多仓库方案指定 `primaryMemberId`。
2. 批准方案后，在 `START_DIRECT_FILE` 确认卡片中选择验证使用的 Codex 后端、模型和思考等级。这份配置只用于验证，文件修改仍由 GPT 完成。旧版单仓库 v1 流程则在批准卡片中选择这份验证配置。
3. GPT 提交 `file_implementation_result_submit`。Core 保存不可变文件结果及快照，关闭写权限，然后创建并启动独立 Codex 验证。该 Execution 没有虚构的 IMPLEMENTATION Run。
4. Codex 在原工作区的只读授权下运行检查。查看原 Execution / VERIFICATION Run 的报告；`SUCCEEDED` 是运行完成状态，检查通过、失败及证据完整性由报告分别说明。
5. 只有必需检查的命令结果等证据齐备、工作区与文件结果快照一致时，才允许用户明确验收。验收继续使用文件实施原有的提交与交付路径。

提交响应丢失时，重放相同 `requestId` 和内容会返回原结果与验证身份。只读的 `file_implementation_result_get` 不启动工作。若提交返回 `READY` 或 `verificationStartError`，先读取原 Execution；在仍为 `VERIFICATION_READY` 时使用准确目标调用 `verification_continue`。它保留原配置，不创建第二个验证周期。运行失败、缺失证据或文件漂移时，按 Core 返回的恢复／重新审查路径处理，不把结果改成通过。

当前 `REQUEST_REVERIFICATION` 资格依赖成功的 Coding Implementation 及其后继关系。Direct File 的冻结文件结果不能冒充 IMPLEMENTATION Run；失败后应读取准确可用动作，必要时通过同一任务的补充审查修订验证方案，再由用户批准。

## 写完整验证指令

Core 原样发送获批的 `verificationSpec.codexInstruction`，不会把另存的 `checks`、`acceptanceCriteria` 或 `scope` 自动拼进提示词。方案作者必须在该指令中完整写入要执行的命令、全部验收标准及要求。

Codex 使用自然输出，不必按 JSON 或固定格式回复。运行结束后，GPT 使用 `run_analysis_get` 读取准确 Run 的结果和观测证据，通过 `run_analysis_submit` 提交单独的 VerificationReport v2。该分析必须逐项覆盖已批准的 `checks` 和 `acceptanceCriteria`，顺序相同，`index` 从 0 开始；不得合并或遗漏标准。分析格式错误可以更正分析，不必让 Codex 重跑。

必需 `CUSTOM` 指令保持必需，不必改为可选或凭空增加命令。Codex 按批准的指令实际验证，GPT 逐项报告结果、证据和限制；不能实际执行时如实记录 `BLOCKED`、`NOT_RUN` 或 `UNKNOWN`。它们只作为分析结论保存，不伪造 Core 观测事实或退出码。明确的命令（包括 CUSTOM 命令）仍需对应仓库、工作目录、实际命令及退出码。命令证据缺失时，模型声称通过也不能替代。

报告完整不等于验证通过。只有必需检查和验收标准均通过、命令证据及工作区连续性校验满足时，才提供“完成任务”及合入确认；必需 CUSTOM 仍受阻时，应延续或暂存任务。审批、验收、合入都保留正式用户确认。

## 旧方案

历史报告、已确认的规格和明确豁免记录不会被自动改写。`NONE` 仍表示没有执行验证，不能展示为已通过。新需求是执行验证时，应保留 `RUN` 与必需检查。

旧报告和未批准方案中的必需 CUSTOM 指令可保留原文重新准备，旧拒绝回执不等于已经批准。运行方式、范围或验证内容需要改变时仍通过正式方案确认；不能静默删除检查、改为可选或添加未获准命令。

Codex 验证通过不等于用户已经验收，验收也不等于领域合并已完成。
