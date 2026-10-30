# Task 历史确认占用恢复

日期：2026-10-25。修复 `.75` / schema133 在真实原任务仍处于 CONFLICT 的问题，已部署 `.76` / schema134，组件沿用面板 v43 / 确认单 v13。

## 原因

原 Task「重构 Progressive Discovery 为节点驱动流程」的旧补审确认 `confirm_c28fa39e39b5743cf3d6ae9ad7e0d89a`，REQUEST_REVIEW 已完成，START_REVIEW 曾以 REVIEW_DEPTH_TOO_SHALLOW 被拒。后续正式 Review 已通过 `task_supplemental_review_starts` 消费同一个补审请求，但 `.75` 的安全释放证明只覆盖仍停留在 REVIEW_REQUIRED 的场景。schema133 又将所有已消费但保存为 PARTIAL 的历史请求纳入冲突，其中六条启动已有准确运行交接回执。

旧冲突分支只在读取时归约完成回执，既没有识别同请求的正式后继审查，也没有将已证实的历史状态与唯一任务头一起落库。结果是：原会话可见的未提交延续方案不可选择，用户无法靠刷新或重试恢复。

原 GPT 随后提交了最新 Verification Run 的分析，任务代际从 1 变化到 3。原未提交延续方案仍绑定代际 1，不能在恢复时直接赋予它当前提交资格。

## 修复

- 共享纯事实归约：读投影、写事务、显式冲突处理和迁移都读取当前 runtime_authority 的 epoch，按准确原回执核对，不以最新 Task 指针或查无回执代替业务完成。
- 为旧浅审查失败增加严格的后继证明：绑定原 Session、Task、Project、FeatureArea、补审 request、源阶段/定义/Review/Execution 及原 purpose；必须存在同请求的 DEEP Review、准确 ACCEPTED 启动和对应 Run，原失败启动不得有回执或 Review/Run 副作用。仅写不可变释放回执，不把原 PARTIAL 改成成功。
- schema134 在同一迁移事务中保存准确恢复为 COMPLETED 的历史确认状态，保存安全释放证明，并重建唯一任务头。只有多个真实活动候选时才继续要求显式选择；未知已消费副作用持续保留。
- 已过代际或过期的未消费候选以不可变原因记录退出当前链路，冻结内容和旧请求不改写。旧按钮仍拒绝；后续新方案读取新代际。
- 纯读取不写确认、操作头或释放回执；正常新命令及确认提交使用同一归约，避免读写可操作性不同。

## 验证

使用生产数据库只读一致性临时副本，未调用模型。schema134 的结果是六条准确启动回执从 PARTIAL 恢复为 COMPLETED；旧浅审查 PARTIAL 与旧未提交延续方案原始 JSON 逐字不变，所有冻结内容及 commit 身份不变。原 Task、Run、业务回执等业务表不变；原 Task 冲突归零，代际推进到 4，旧代际方案被封住。临时数据库随后移除，不作为生产备份保留。

真实 Core 回归复现 v133 多候选，覆盖同补审请求正式接续后恢复、重复迁移、纯读不写、原冻结提案保留、恢复后正式提交，以及没有准确拒绝证明时仍不放行。另覆盖已过代际的多个未消费提案可以退出、已消费 UNKNOWN 不能退出、旧卡片迟到提交失败与新准备可用。

专项 Core 回归 PASS（39.708s）；扩大确认与恢复回归 PASS（165.395s），MCP 全包 PASS（78.970s），Console API 全包 PASS（5.169s），HTTP 全包 PASS（116.038s），修改包 go vet 与 git diff --check 通过。SQLite 全包 PASS（320.743s），隔离候选启动通过。原始证据位于 `build/history-action-repair-20261025/` 与部署候选目录 `build/full-flow-update-20261025-76/`。

## 生产部署与原 GPT 实测

2026-10-25 21:39（Asia/Shanghai）停止无活动 Run 的托管服务并部署 `0.1.0-dev.full-flow-20261025.76+gc4ae1d3`。生产自动执行 schema134，integrity_check=ok；Core、GPT MCP、本地 MCP 与隧道就绪，实际连接器诊断及 initialize 均返回 `.76`。部署前后 31 张 canonical 业务表内容哈希相同，六条准确交接确认归并、两条不可变释放回执与临时副本预演一致；所有原冻结方案及 commit 身份保持不变。

实际沿原 Session 重新读取后，原 Task 为 ACCEPTANCE_PENDING、最新验证分析 RECORDED、操作节点 AVAILABLE / generation4、CONFLICT 已解除。CONTINUE_TASK 和 DEFER_TASK 可用，COMPLETE_TASK 因必需验证未通过仍不可用。

随后只准备了一张基于当前 Task/Execution/Revision11 的新延续确认：`confirm_1c6e617c8b2e286227648046b0429f00`，版本1，PENDING；准确 options_get 返回 eligibility.available=true，唯一方案 available=true。旧未消费确认仍保留原 JSON，因过代际显示 STALE，不能迟到提交。新确认未被消费，也没有启动工程 Run。

已只读核验原 GPT 对话「同步项目会话信息」的实际右侧工作台：后续操作只有1项「延续任务 · 待确认」，显示可用的蓝色「确认延续原任务」按钮。浏览器操作只查看和滚动，没有提交确认、发送消息或改动原会话内容。证明截图与完整证据位于 `build/full-flow-update-20261025-76/` 的 `original-gpt-continuation-ready.png`、`original-gpt-continuation-ready-panel.png`、`task-recovery-readback.json`、`deployment.json` 和各阶段快照。

本次解除的是 DevBridge 历史确认占用。Skeleton 本身的工程验证结论仍为 REVISE/BLOCKED；用户正式确认延续后，GPT 才沿原任务准备后续实施或重验方案，其启动仍受原正式确认及执行门禁控制。
