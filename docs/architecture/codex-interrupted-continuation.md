# Codex 中断后的续作

更新：2026-10-25。task-panel-v39 已随 `.70` 部署。部署和只读回验见 [部署记录](../validation/review-result-read-2026-10-24.md#部署回读)。

Codex 已中断但任务尚未结束时，GPT 工作台显示原运行及当前续作状态。文件改动、任务定义、历史运行和确认回执保留。

## 用户入口

- `AVAILABLE`：显示“Codex 已中断，可以继续”和“继续 Codex · 准备确认”。点击后请求 GPT 核对原 Run 的输出、文件改动和当前资格，必要时完成正式分析，再在原面板准备续作确认单。用户正式确认后才启动后继运行。
- `RECOVERY_REQUIRED`：显示“Codex 已中断，待核对后继续”。原生结果未知或不可读时，先处理准确恢复资格，不展示可直接续作的按钮。
- `UNAVAILABLE`：显示“Codex 已中断，暂不可继续”。原任务被暂存、进入交付或当前 Core 不允许续作时，处理已有决定及阻塞。

点击后的消息继续使用持久交接日志和固定 prepare requestId。宿主接收消息仅表示已发送，只有准确请求对应的确认单入库才表示准备完成。刷新、重复点击、宿主超时均不自动重发。已有当前续作确认单时，面板引导用户处理该确认单。

## 续作合同

| 中断阶段 | Core 现有动作 | 确认后的行为 |
| --- | --- | --- |
| 实施 | `RETRY_IMPLEMENTATION` | 同一 Task、同一冻结定义与后端，使用原托管工作区中的已有改动，启动后继 Execution/Run；原安全 Thread 由既有连续性协议恢复 |
| 验证 | `REQUEST_REVERIFICATION` | 在原 Task 对现有实现重新验证，保留冻结检查及运行配置 |
| Codex 审查 | `REQUEST_TASK_REVIEW` | 在原 Task 准备接续审查方案，正式确认后建立新的审查 |

续作入口仅复用已经完成身份校验、权限检查和运行资格检查的当前能力，不新增启动 API，也不绕过正式确认。后继运行有自己的 Run/Turn 身份；已终止的原 Turn 不被改为运行中，旧中断记录不被覆盖。仍在运行的原 Turn 继续由现有 App Server 自动重连协议处理。

范围变化需要新方案；不清理工作区、不重置改动、不创建替代 Task、不重放旧审批或旧命令。用户主动取消、一般工程失败、历史运行、只读任务和其他执行后端不会被标成可继续的 Codex 中断。

## 原生中断归因

真实 `turn/completed` 返回 `interrupted`，且 Thread/Turn 与本轮准确匹配时，非用户取消的运行记录 `CODEX_TURN_INTERRUPTED`，归为运行基础设施中断，避免将其误报成工程检查失败。已有明确取消回执继续记录 `EXECUTION_CANCELED`。收口、权限或结果仍未知时，不因这个标识授予续作权限。

Bridge 已有的 `bridge_shutdown_interrupted`、`bridge_restart_interrupted` 同样参与展示；不扫描错误文本猜测历史运行结论，不修改历史记录，不需要数据库迁移。

验证记录见 [中断续作验证](../validation/codex-interrupted-continuation-2026-10-24.md)。
