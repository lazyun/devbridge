# 旧 Go 测试夹具修复（2026-10-22）

自然输出与 GPT 独立分析接入后，旧夹具仍假设 Codex 最终消息直接生成正式报告，或使用过时的审批、修订和数据库结构。本次修复这些测试前提，未修改生产准入规则。

## 修复范围

- 审查、验证和恢复诊断夹具在 Codex 完成后，通过公开 Facade 显式提交独立编写的 GPT 分析，保留原始输出、命令证据和准确源校验。失败退出码与验收标准失败分别覆盖，不能被“通过”声明覆盖。
- 任务创建、修订、审批与无 Run 后继夹具使用当前草稿修订、正式确认和运行资格流程；不以旧的标量身份或缺失授权替代当前绑定。
- 必需验证使用可观测命令，替换不受支持的必需 CUSTOM 检查。
- 历史迁移夹具使用对应历史版本的真实迁移建立数据库，再升级当前版本；不再通过删除当前结构的部分表模拟旧库。
- 更新合入重试、响应丢失、冷重启和新审查回合的断言，保留冻结计划、调用者隔离、原始回执与显式重试约束。
- 共享工作流夹具仅缓存由真实迁移生成的空数据库结构；每例独立数据库，正常初始化仍分配运行纪元与锁，不复用会话、资格或业务记录。

## 验证结果

主模块共 97 个包，默认 Go 回归已分批复验通过，并非一次完整 `go test ./...` 调用的结果。

| 范围 | 结果 | 本机原始日志 |
| --- | --- | --- |
| 除 Control API 外的 96 个包 | 整批退出 0 | `build/fixture-other-packages-final.log` |
| Control API 全部 394 个顶层测试 | 364 通过、30 默认跳过、0 遗漏、0 剩余失败 | `build/fixture-controlapi-final.jsonl`、`build/fixture-controlapi-remaining-final.jsonl` 与下列修复后复验 |
| 补充验证、停止旧审查后新建审查、失败证据下显式验收 | 三项分别退出 0，覆盖前批次最后三项失败 | `build/fixture-reverification-final.log`、`build/fixture-review-stop-final.log`、`build/fixture-verification-semantics-final.log` |

覆盖核对以编译后的 394 个顶层测试名称为基准，合并批次结果，并以修复后的定向复验覆盖对应旧结果。汇总保存在本机 `build/fixture-final-summary.json`。原始日志是忽略的本机构建材料，不随本文提交。

相关包 `go vet` 通过，最后修改的 `internal/controlapi` 与 `internal/workflowtest` 再次检查通过（`build/fixture-vet-late-final.log`）；相关改动 `git diff --check` 通过。

默认跳过项包括真实 Codex、原生进程连续性及需要额外环境的场景；本次未启用这些场景，不把跳过计为通过。先前自然输出接入的隔离原生验收仍见[原验收记录](natural-codex-results-2026-10-22.md)。

本次未部署服务、修改正式业务数据库、执行真实业务任务、提交或推送仓库；未触及 `skeleton/`。
