# execution_prepare 参数与验证失败诊断（2026-10-21）

## 结论

本轮同时发现参数构造错误、公开 Schema 与 Core 不一致，以及后续验证方案不能通过执行准入的问题。

- 原 Work 对话的六次失败调用没有一次包含正确的完整组合：缺失 `variants[i].implementationMode`，或把模式误放在 `execution`，或缺少完整规格。
- 模式同时位于 `variants[i].implementationMode` 和 `variants[i].workSpec.implementationMode`，两者必须一致。`execution` 不接受该字段。
- 旧公开 Schema 允许 `APPROVE` 使用只有名称和摘要的决定型方案，而 Core 要求完整实施方案。旧 Schema 还错误地让 Direct File 操作使用决定型方案，并要求首次启动携带恢复标识。
- 正确参数通过旧版 Schema 后，生产任务进一步返回 `CONFIRMATION_FACTS_UNAVAILABLE`。数据库只读核实其正式报告为 `DIRECT_FILE`，验证为 `RUN`，唯一检查是没有命令的必需 `CUSTOM` 静态阅读检查。
- 当前执行准入拒绝必需 `CUSTOM` 检查；本轮保留该规则，以类型化的 `UNOBSERVABLE_REQUIRED_CHECK` 返回准确原因，不再包装为事实不可用。

## 修复范围

- 按 action 区分完整实施方案与验收决定型方案，消除匿名 `oneOf` 对缺失字段的遮蔽。
- 校验方案内两处实施模式一致，Direct File 操作携带一个完整 Direct File 方案。
- 初次 Direct File 启动不携带恢复标识；恢复和取消要求精确工作流、运行代际与正数授权代次。
- 工具说明和接口文档明确字段路径、完整规格、准备与批准边界，以及必需检查的验证限制。
- 为不可观测的必需检查保留可识别错误身份，经过 MCP 返回准确失败码与 `PREPARE_NEW_VERSION`。

没有修改原任务的报告、约束、必需检查或验证模式，没有推断用户豁免，没有消费批准或启动实施。

## 验证

| 检查 | 结果 |
| --- | --- |
| 修复前新增 Schema 回归 | FAIL，复现空批准方案被接受、Direct File 完整方案被拒和模式不一致被接受 |
| `go test -p 1 ./internal/task ./internal/mcpserver` | PASS |
| `TestExecutionPrepareWireRequiresCompleteReviewedPlan` | PASS，真实 MCP HTTP、SQLite 和 Core；拒绝空方案，返回准确验证失败，并成功准备可执行方案 |
| `TestMultiRepositoryDirectG1G4G5G6ProductionClosure` | PASS，Direct File 授权与实施闭环 |
| `go build -p 1 ./cmd/...` | PASS |
| 修改文件 `git diff --check` | PASS |

真实 MCP 测试同时确认准备后 Task 仍为 `REVIEW_APPROVAL_PENDING`，没有 Execution、文件授权或提交回执。测试使用临时数据库与本机临时监听。

扩大检查 `TestMultiRepositoryDirect*` 时，两项未通过：
`TestMultiRepositoryDirectG6AreaMergeEffectBeforeReceiptUsesFrozenPlan`、
`TestMultiRepositoryDirectG6PartialDirtyTargetRecoversOriginalMapping`。
两者均在切换开发映射时返回 `GIT_FINALIZATION_PENDING`；它们直接测试 Core，发生时本轮仅修改 MCP Schema。本轮未修复这些交付恢复问题，不宣称全量回归通过。

## 本地服务与真实任务

已构建并更新三个托管二进制，保留上一版供回退，经 Desktop 正常重启。
实际隧道 `bridge_diagnostics` 确认版本为
`0.1.0-dev.events-20261021.3+gd3969cf`、`ready=true`。

重启前没有活动 Run；真实任务保持 `REVIEW_APPROVAL_PENDING`、没有当前 Execution。
任务的 MCP Events 订阅保持 `ACTIVE`。

更新后，在原 Work 对话使用同一完整方案、仅更换请求身份单次复验，实际返回：

```json
{
  "businessOutcome": "REJECTED_NOT_PREPARED",
  "failure": {
    "code": "UNOBSERVABLE_REQUIRED_CHECK",
    "message": "UNOBSERVABLE_REQUIRED_CHECK: CUSTOM must be optional",
    "nextAction": "PREPARE_NEW_VERSION",
    "source": "DOMAIN",
    "stage": "PREPARE"
  }
}
```

上述为返回节选。原对话已说明准确阻塞原因，没有重试或消费批准。

正确参数仍不能使当前静态验证方案变成可执行方案。后续需要用户决定如何修订验证要求；不得通过把检查改为可选、增加未授权命令或添加验证豁免来绕过。
