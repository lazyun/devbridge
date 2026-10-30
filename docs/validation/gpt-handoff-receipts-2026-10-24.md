# F6 GPT 交接日志与业务回执

原有修复已保存正式确认后的待交接队列，覆盖回读失败后续接；残留在通用
sendConversationContext：conversationAttempts 仍只在内存、部分 key 使用界面 generation，
ACK 后缺少统一业务产物跟踪，审查/分析/文件实施没有可恢复的显式重试入口。

本次修复：

- 在发送前保存原请求、准确 Session/Project/Task/定义与业务目标、稳定 key、prepare requestId、
  消息状态和业务回执状态。异步 setWidgetState 也必须先确认保存，保存失败或不支持时不发消息，
  提示尚未发送，可恢复后显式重试。快照同时保留准备表单原文和需求草稿。
- 同一目标的 key 不再随界面 generation 或普通审查阶段变化。目标、版本、Decision、授权版本
  或需求原文变化时独立处理，不借用旧请求。刷新将 SENDING 恢复为 UNKNOWN，不自动重发。
- 消息状态 SENDING / SENT / UNKNOWN / REJECTED / NOT_SENT 和业务状态
  AWAITING_RECEIPT / RECEIPT_OBSERVED / STOPPED / UNTRACKED 独立。
  SENT 只代表宿主已接收；RECEIPT_OBSERVED 只代表对应产物入库，不代表审批、验收或整个 Task 完成。
- 准备请求按原 Session/project/requestId 关联准确 intent/version/contentHash；
  GPT 审查按原 Task/定义/Review/Run/reportId；正式分析按原准确 Run 的 RECORDED；
  文件实施只读查询原 Task/定义/workflow 的 file_implementation_result_get，核对准确结果身份。
- 迟延 ACK 或发送错误不覆盖已读到的业务回执。读取失败保留等待记录，不自动补发消息。
  文件结果读取有上限的失败退避；原生结果 UNKNOWN 不因宿主 ACK 改写。
- 显式重试在点击时重新核对原目标、操作资格及重新生成的原文；沿原 prepare requestId。
  目标/内容已变、回执已存在或读取事实失效时，不通过旧按钮发送。
  已结束或被替代的目标停止等待，但不称其成功；历史排队交接不会从展示快照获得新授权。
- 旧面板保存过的 SENT/UNKNOWN 交接保守迁移为去重记录，并重新跟踪准确产物，不自动再发。
  无法从旧版本恢复其从未保存的通用消息内容或制造旧业务回执。

实现边界：

日志保存于同一准确展示入口的宿主 widgetState，是有界展示快照，不是 Core 的 durable binding，
也不承诺跨设备、丢失宿主快照或多客户端的原子消息恰好一次。Core 继续保存正式业务对象与不可变回执。
日志最多保留 20 条，优先清理已观察产物、已停止或无正式回执的历史记录；未解决记录满额时明确提示，
不会静默丢弃尚在跟踪的交接。没有正式写入产物的纯对话分析标为 UNTRACKED，不虚构业务完成回执。

验证日志：build/gpt-handoff-receipts-20261024/。
新增故障测试覆盖发送前保存、真实重新挂载恢复、未知发送显式重试、明确拒绝、
保存抛错/异步失败/不支持、异步保存顺序、迟延 ACK/错误、准确准备/报告/分析/文件结果、
外来快照、伪造完成声明、旧交接迁移、旧按钮与篡改原文。没有向真实 GPT 对话发送测试消息。

验证结果：

- 面板 JavaScript：405 项，401 通过、4 跳过、0 失败。
- internal/mcpserver 全包通过（48.314s）；相关 controlapi 回归通过（8.807s）。
- go vet internal/mcpserver 通过；最终面板资源测试通过（1.628s）；git diff --check 通过。

部署结果：

- 已部署 0.1.0-dev.full-flow-20261024.67+gd3969cf，主面板资源 task-panel-v37，脚本 2.25.0。
- Core、GPT MCP、本地 MCP 和 OpenAI 隧道均就绪；通过 MCP resources/read 读回新面板，
  核对交接日志、业务回执和显式重试实现。此验证没有发送真实 GPT 消息。
- 1726 个构建源文件与构建清单一致，三个已安装二进制与候选哈希一致。
- SQLite schema 132，integrity_check=ok；部署前后 22 张业务表摘要一致，无活动 Run。
  原任务仍为 ACTIVE / ACCEPTANCE_PENDING，原方案确认仍为 PENDING，没有消费确认。
- 回滚备份包含 .66 的三个二进制及一致性数据库备份，位于
  build/full-flow-update-20261024-67/before-update/。

部署核验记录：build/full-flow-update-20261024-67/deployment.json 和 verification.json。
已有旧面板需要重新打开以加载 v37；旧版本从未保存的通用交接无法追溯恢复。
