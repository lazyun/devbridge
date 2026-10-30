# SCOPE_CONFLICT 后续处理指引修复

已确认问题：实施失败时 Core 有停止与恢复门禁，但面板和 GPT 指引未说明
是否重审、是否可重试及范围修订的准确分流。既有行为并不是“一律重新审查”。

已修复：

- 准确当前 Implementation 的终态 SCOPE_CONFLICT 在面板显示“核对实施范围冲突”，
  提醒保留已改文件、核对失败原因与获批范围，说明同版本续作、正式修订及条件补审。
- 原生结果 UNKNOWN 独立保留，仍先找回结果；提示不将失败、已知结果、缺失结果混同。
- Core 的 RESOLVE_TASK_RECOVERY 提供同样的范围冲突说明，不添加启动、验收或补审权限。
- GPT 合同明确：已解决的 CONSISTENT / RECOVERED_PARTIAL 才可按资格同版本续作；
  REVISION_REQUIRED 走绑定的 RECOVERY_PURPOSE 正式确认，不能用普通 REQUEST_CHANGES 绕过。
  该正式恢复修订不会自动新增 Review。需要补审时只使用 Core 明确提供的同任务资格。
- 实际越界、漂移、CONFLICT、INSUFFICIENT 或原运行不具备恢复条件时，说明具体阻塞，
  不承诺自动重审或续作，不 reset/clean 文件、不新建任务绕过。
- 补充架构、用户流程、接口及当前合同，并纠正用户流程中仍残留的旧“不审查需两次确认”说明。

完整规则：[实施范围冲突分流](../architecture/engineering-scope-responsibility.md#implementation-scope_conflict-handling)。

本次不改变写侧准入、生命周期、恢复评估矩阵或审批策略；没有新 schema。
范围冲突处理说明只来自当前准确 Run 的正式失败码。历史运行、其他 Execution、不同定义、
Review/Verification、未终结运行和普通文本中的同名词不能借用该说明。

验证与部署：

- 展示层全量回归 PASS（0.503 秒），覆盖已收到、未知及不可用结果，以及跨身份/普通文本负向。
- SQLite 相关 Task Recovery、Scope Conflict、Implementation Revision Required 和 Recovery Purpose
  回归 PASS（16.119 秒），继续验证普通重试/验收受阻、同版本续作和正式后继修订。
- controlapi 恢复后继与不审查单次启动集成回归 PASS（15.817 秒）。
- MCP 全量 PASS（50.440 秒）；面板 JavaScript 393 项，389 通过、4 原条件跳过；go vet PASS。
- 已部署 .66，schema 132，继续使用 task-panel-v36；面板 HTML 未变，新增提示由 Core 数据返回。
  Core、MCP、本地 MCP、隧道均 READY，MCP initialize 已返回新的范围冲突分流合同。
- 更新前后 22 张业务表摘要、原 Task、待确认方案身份和 PENDING 状态不变；无活动 Run，
  未消费用户确认，未向原 GPT 对话发送新消息。保留 .65 二进制和一致性数据库备份。
- 部署核对仅为只读服务、资源与数据检查；范围冲突场景由隔离测试验证，没有在真实任务制造冲突。

日志与摘要：build/scope-conflict-guidance-20261024/；
部署与回滚：build/full-flow-update-20261024-66/。
