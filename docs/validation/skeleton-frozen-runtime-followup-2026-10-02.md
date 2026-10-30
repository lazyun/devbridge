# 运行约束后续审核与修复（2026-10-02）

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-15f5a80c13eb8870)。

本轮继续审核上一轮修复后的生产实现，确认并修复以下 4 项缺陷。`skeleton/**` 只读，未运行其测试或整包回归。改动保留在当前工作区，未提交。

## 修复范围

1. **启动凭据过期仍可启动**：取得 qualified native token 后，原资格被撤销、后端被禁用或指纹变化，旧 token 原先仍可启动进程。服务层现在在实际消费 token 时重新读取原 owner 的完整绑定投影及后端状态，再验证并使用已捕获的 native 制品。原绑定的 SUPERSEDED 资格仍可使用；不重新选择最新资格、provider 或 selector。
2. **验收成功但确认响应丢失无法恢复**：`RESOLVE_ACCEPTANCE_DECISION` 原先没有 UNKNOWN 恢复分支。现在只读核对原提交、原 actor/request、Decision 和目标修订，恢复 ACCEPT、REJECT、REQUEST_REVERIFICATION 的精确结果。重新验收还校验原唯一 successor 和 NO_START 效果。缺回执或只有另一请求的回执时保持 UNKNOWN，不重复执行业务或交付。
3. **正式批准被提前显示为完成**：通用 APPROVE 恢复原先把 T1 的批准回执当作整个确认完成，忽略 T2、T3 和后续运行绑定。现在正式确认必须有原最终 archive、source 和 binding 才能恢复为 COMPLETED/PREPARATION_READY。已知部分事务保留原身份供继续处理；最终确认响应丢失则只读恢复既有完整结果。历史 journal 即使提前记录了 COMPLETED step，也必须经过同一校验，不能借外层状态跳过绑定。
4. **旧 schema 104/105 缺少后来补充的约束对象**：真实开发旧库曾在结构尚未完整时记录 version 104，导致升级失败，或升到 105 后仍缺正式采用 fence/guard 和原 NO_START 命令身份表。v105 前置处理及 v106 前向迁移只修复精确识别的旧 DDL，补全空结构，不推造历史授权。完整结构及数据保持原样；未知结构、额外未识别对象或存在无原始 fence 的历史采用数据时拒绝迁移，并回滚保留原库。

## 验证证据

除迁移证据外，本轮日志位于 `build/constraint-followup-2026-10-02/`。

| 验证 | 证据与结果 |
| --- | --- |
| 真实 native 启动凭据失效 | `native-live-token-red.log`、`native-live-token-green.log`；禁用、指纹变化、INVALIDATED 三个反例修复前失败、修复后通过；初始、SUPERSEDED、selector 改变三个正例通过 |
| 验收最后一次 RecordStep 故障 | `acceptance-recovery-red.log`、`acceptance-recovery-green.log`；三个验收动作冷读及原请求重放通过，另一请求不能借用结果，缺回执不重发；全表快照不变 |
| 正式确认四个故障窗口 | `formal-confirmation-red.log`、`confirmation-recovery-boundaries-final.log`；T2、T3、binding、最终确认落盘失败均通过；公开冷读与原请求继续没有重复效果或新增 Run |
| 历史提前完成步骤不能绕过绑定 | `confirmation-completed-step-red.log`、`confirmation-completed-step-green.log`；现有 SQL 约束确实允许该历史 journal 形态，新读检查拒绝误报完成，保留原状态及身份、零写入；与四个故障窗口合并通过 |
| 既有正式批准/采用并发及冷恢复 | `formal-recovery-regression.log`；4 个顶层用例通过 |
| 既有定义审批和 retired UNKNOWN | `confirmation-recovery-boundaries-final.log`；6 个顶层用例通过，包括定义审批事务失败、历史基准、旧 epoch 回放及未产生效果的已消费请求 |
| 两份真实旧库修复及映射回归 | `build/constraint-gap-fixes-2026-10-02/schema104-upgrade/` 中 `red.log`、`green-first.log`、`migration-and-mapping-final.log`；6 个顶层迁移/映射用例通过 |
| 已执行旧 SQL-only v105 的独立修复入口 | 同迁移目录 `independent-105-to-106.log`；确认旧 105 仍缺结构后直接升到 106，原业务行与资格映射保持不变，冷重放零写入 |
| 完整生产流程库历史数据保存 | 同迁移目录 `canonical-preservation.json`；升到 106 后 163 张业务表逐行 SHA 一致、无外键错误、原数据库文件未改变 |
| 产品编译及静态检查 | `production-compile-final.log`、`affected-go-vet-final.log`；仅 `./cmd/...`、`./internal/...` 编译检查及受影响包 vet |
| 最终源码打包公开生命周期 | `packaged-native-final.log`；25.756s 通过；真实 native、隔离本机 Responses 服务，NO_START、显式采用、两次成功运行、ACCEPT、Task 提交、FeatureArea 合并和冷重放 |
| skeleton 冻结 | `skeleton-read-only-verification.json`；470 个文件 SHA 全部一致，无新增文件 |
| 工作区格式检查 | `final-workspace-checks.json`；122 个改动 Go 文件 gofmt 一致，`git diff --check` 通过 |

真实 native 用例只使用本机回环服务和隔离配置，不调用线上模型。协议 fixture 用于状态机验证，不作为 native 兼容证据。本轮是上述缺陷的定向验收，不代表项目全部测试或所有剩余路径都已验证；既有 continuity 版本支持范围保持原状。
