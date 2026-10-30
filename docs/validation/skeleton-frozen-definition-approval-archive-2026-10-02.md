# Frozen Skeleton：定义审批的不可变归档

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-9cbbd715845787d8)。

日期：2026-10-02。任务类型：`IMPLEMENT_FROZEN_SKELETON`。

## 范围与契约

接续[审批选型与确认终态修复](skeleton-frozen-approval-selection-completion-2026-10-01.md)。Freeze commit 保持 `4406e717949af5bce6547af0b71525b74e206c81`，Skeleton tree OID 保持 `942d056a9c78a7e1b28319f4e535bdc893b3509a`，不修改 `skeleton/**`。

本批实现新准备的 v2 `CODING_AGENT`、`APPROVE / NO_START` 定义审批归档，要求原父 revision 真实存在，且 Review 的持久启动来源能证明它审查了该 revision。它记录定义审批结果，不产生平台资格、runtime selection source、revision binding、Execution、Run、ScopePlan 或 PreparationAuthority。

冻结依据是 `runtime_platform_formal_business.go` 的 `APPROVE_DEFINITION_ONLY` 分支：该分支取消平台资格前置，但保留精确定义、Decision、lineage 和提交证据，并明确分成 `REVIEW_APPROVAL_TRANSACTION` 与 `CONFIRMATION_RECEIPT_TRANSACTION`。`CONSUMED` 和通用步骤 `COMPLETED` 均不能单独替代这些业务证据。本实现使用生产领域类型和 SQLite 事务，不导入或执行 Skeleton 的 witness evaluator，也不宣称其他 formal 分支已经实现。

## 生产实现

`internal/formalapproval` 定义有限的 Core schema catalog、PrepareFence、BusinessSnapshot、Receipt 和精确读取身份。Catalog 按 schema ID、version、purpose、action 唯一匹配，并使用实际 `task.Definition` decoder 验证完整定义及其规范投影。不同 hash 域分别保留：Prepare 原字节、normalized CommitInput、Core 原始 submitted fingerprint。原请求 JSON 是 typed projection，不能重新编码后替代原始 submitted hash。

Prepare 同一事务保存父定义原字节/hash、真实 DecisionVersion 和 frozen content。归档适用性由 Core 已保存的 Review 提案决定；加入未选中的 Direct File variant 不会使 Coding 审批退回无归档路径。事务消费 Decision 前再次读取版本和当前 owner，核对 session、原 commit、唯一选中 variant、APPROVE 子请求及 Review/revision 来源。

审批事务原子提交 Decision 消费、新定义 revision、Task phase 和不可变业务快照。快照写入失败时，整个审批事务回滚。随后独立的正式回执事务从原快照封存 Receipt；其失败只留下已经提交的业务快照。恢复先尝试封存原快照，成功后投影原 Task/revision，不重新审批，也不借用当前 Task 状态重建旧结果。

v99 增加 fence、business snapshot、receipt 三张表，带来源约束、外键及禁止更新/删除的保护。正式回执另存实际内容 hash。已登记此归档流程的确认，必须有精确最终回执，且 Task/revision 输出一致，才可将 APPROVE 步骤记为完成。通用 confirmation 仍保留原步骤接口；两个正式业务事务各自提交。

历史读取和已完成 Finalize 重放先纯读原记录，不经过 writer fence。校验原 immutable intent、请求账本、Review admission、Decision receipt、父/子 revision 和归档三表之间的对应关系，不用当前 Task owner、当前 session 权限状态、实时 Decision、当前 schema eligibility 或平台资格替代原证据。权限检查由调用方承担，未新增公共传输端点。对重复 JSON 键、未知字段及大小写/Unicode 别名，领域解码和原 journal 读取均拒绝歧义。

## 兼容与未完成范围

首次审批缺父 revision、v1、Operator、旧 intent、非 Coding Review 提案、缺少可证明 Review/revision 启动链的历史流程，保留既有业务行为，不补发新归档。Review revision 来源目前覆盖正式 supplemental request/start，以及携带精确 expected revision 的原 START_REVIEW confirmation。旧记录缺证据时，正式读取返回缺失，后续消费者不得据此推导授权。

审批事务失败且没有业务快照时，原通用步骤可以保持 `UNKNOWN`；读取或重复原请求不会把它自动重试成成功。只有已提交的精确业务快照支持续做回执事务。该边界不改变既有未知业务命令的恢复策略。

真实兼容性 collector、平台资格与原提交的精确关联、带运行授权的 formal source、revision binding、`APPROVE_AND_START` T1/T2/T3 仍未实现。此定义审批归档可作为后续 prior-approval 消费者的原始证据，但本批不接通这些消费者。

## 验证

证据目录：`/private/tmp/devbridge-platform-next-23nlew3b`。所有既有未提交修改保留，本批不提交 Git。

真实 Core 测试通过 Upper Review 读取证据和提交报告，首次 NO_START 生成 v2 parent，再经正式 supplemental Review 进行本次审批；不直接插入成功 revision 或 receipt。故障用例在真实写入边界安装临时失败触发器：

- business snapshot INSERT 失败：Decision、revision、materialization、Task 指针全部回滚；原请求保持 UNKNOWN，重复读取/提交零新增业务记录。
- receipt INSERT 失败：保留已提交 snapshot，冷重启后仅封存回执。
- 最后 RecordStep 保存失败：业务快照和回执保留，重放恢复原输出。
- 冷重启、精确身份/hash/version 冲突、SQL 不可变性、无父 revision 不补档，均有实际流程测试。
- 过期 callback context 下 Read/Finalize 返回原结果，`total_changes()` 和全表快照不变；相同 context 的新 Prepare 被拒绝。这不等同于仍持有旧 DB writer 句柄的测试。
- 第三次真实 Review/审批推进到新 revision 后，第二次遗留快照仍可冷封存；原请求返回第二次 revision，当前 Task 和第三次回执不变。

混合 variant 回归用修复前 capture helper 的 Go overlay 复现 `fences=0`，再在当前实现验证未选 Direct File variant 无法排除正式归档。测试仅使用本地文件和测试协议后端，不是线上模型兼容性测量。

最终结果如下，测试节点含子测试，专项之间存在重叠，不合并计数：

| 检查 | 结果 | 日志 |
| --- | --- | --- |
| SQLite 全包 | 1086 pass，0 fail，0 skip；158.462s | `storage-domain-final.jsonl` |
| formalapproval 领域全包 | 18 pass，0 fail；0.802s | `storage-domain-final.jsonl` |
| confirmation 全包 | 49 pass，0 fail；1.392s | `storage-domain-final.jsonl` |
| Core 审批/显式启动/NO_START/Direct File 兼容专项 | 38 pass，0 fail，0 skip；81.752s | `controlapi-final.jsonl` |
| 最终归档集成专项，含最后的 mixed-variant 反例 | 8 顶层、16 节点全部通过；31.179s | `formal-archive-green.log` |
| mixed-variant 修复前 overlay | 同一新测试失败，`fences=0`；2.957s | `mixed-variant-red.log` |
| `go vet` 领域/SQLite/Core API | 通过 | `vet-final.log` |
| `gofmt -l` 本批 Go 文件、`git diff --check` | 通过 | `format-check.log` |
| `make -C skeleton generate-check production-parity` | 通过 | `frozen-parity.log` |

Core 38 节点专项启动编译时尚未加入最后一个 mixed-variant 测试；后者由最终 16 节点归档专项覆盖，生产修复已存在于两次运行中。未重复执行整个 Core API、MCP 和执行层全包；本批没有修改运行器或传输接口。`production-parity` 仅证明现有 Task phase 词汇映射，不代表完整运行流程符合全部冻结契约。

过程中的迁移测试失败已保留并修复：旧 v97/v95 升级 fixture 在当前库删掉旧标记时还保留 v99，导致 MAX(version) 跳过目标迁移；现在先确认 v99 三表为空，再移除这些表、其 completion guard 和版本标记。另一个“未来版本必须拒绝”的测试硬编码了 99，现改为当前最大版本加一，避免与真实新增迁移相撞。新增 v98 → v99 测试验证 pending/completed 历史确认在两次重开后均不改写、不补档。首轮全量失败只涉及上述硬编码未来版本测试，最终全量已通过。一次并行 vet 碰到测试文件编辑中的暂时未使用 import，测试稳定后重新验证通过。

`freeze-proof.json` 记录 470 个 Skeleton 文件与本批起点逐文件一致，`git status -- skeleton` 为空，tree OID 与冻结值一致。`final-delta.json` 限定本批 16 个变更文件，其他既有修改保留。变更留在工作区，未提交。
