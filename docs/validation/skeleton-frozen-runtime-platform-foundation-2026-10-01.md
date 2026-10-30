# Frozen Skeleton：运行平台资格的持久化基础

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-bea7dd5db4db77f1)。

日期：2026-10-01。任务类型：`IMPLEMENT_FROZEN_SKELETON`。

## 基线与本批范围

- Freeze commit：`4406e717949af5bce6547af0b71525b74e206c81`。
- Skeleton tree OID：`942d056a9c78a7e1b28319f4e535bdc893b3509a`；`skeleton/**` 只读。
- 接续[无 Run 修订出口与 Task 取消](skeleton-frozen-no-run-revision-2026-10-01.md)之后的生产缺口处理。既有未提交修改保留。

核对 `APPROVE_AND_START` 部分进度契约后发现，T1 的完整身份依赖 Core 平台描述、独立兼容性证据、资格代次及 revision binding。生产代码已有 backend 配置指纹和 runtime evidence 数据类型，但没有这些资格事实的管理链。配置指纹、静态模型清单、调用参数及 App Server 观察不能替代独立兼容性证据。

因此本批先实现可独立验证的持久化基础及 Core 采集接口边界。没有接入业务启动、没有自动给现有后端发放资格，也没有把现有 APPROVE 与 START 两步流程声明为 T1/T2/T3。

## 实现与契约映射

新增 `internal/runtimeplatform` 类型、校验及 Store 端口；SQLite v97 增加 descriptor、compatibility evidence、qualification、lineage 和不可变 request 记录。平台事实具有独立表和事务，不创建 Task、Execution、Run、scope plan 或 preparation authority。

| 冻结操作 | 本批实现 |
| --- | --- |
| `runtime_platform.register_descriptor` | 精确不可变注册；同 ID 同内容纯读重放；同 ID 不同内容拒绝；同 lineage 的角色固定 |
| `runtime_platform.materialize_compatibility_evidence` | 校验 descriptor、lineage、角色和实现/协议摘要；要求兼容性通过、策略/采集器身份和产物摘要；重新计算证据 hash |
| `runtime_platform.qualify_generation` | 从精确已登记 descriptor/evidence 发放；generation 必须等于高水位加一；lineage version 在同一事务中比较并推进 |
| `runtime_platform.replay_qualification` / `reject_qualification_conflict` | request namespace 防跨操作复用；重放先于当前资格及 CAS 检查；身份冲突零写入 |
| `runtime_platform.supersede_generation` | 精确匹配旧 CURRENT 身份和代次；旧资格替换、新资格插入及 lineage 推进原子提交 |
| `runtime_platform.invalidate_generation` | CURRENT 或 SUPERSEDED 可失效；记录不可变失效请求、原因和时间；禁止恢复 CURRENT |

同一 lineage 至多一个 CURRENT。资格的业务身份与 issuance payload 不可改写；合法生命周期边为 CURRENT→SUPERSEDED、CURRENT→INVALIDATED、SUPERSEDED→INVALIDATED。SQL 同时保护不可变记录、状态边和高水位。

请求记录通过延迟复合外键绑定精确资格 ID、lineage、generation 及对应的 issuance / invalidation request 指针，阻止“写了请求却没有完成对应资格变更”的事务提交。新平台存储固定事务使用的数据库连接；COMMIT 失败后在同一连接显式 ROLLBACK，清理失败则丢弃连接，避免底层未结事务返回连接池。该处理仅应用于新平台存储，沿用已有 writer / Run epoch 检查。

重放返回原请求指向的同一资格代次及其**当前持久化生命周期状态**，不会把已失效资格重新表现为 CURRENT，也不会改写原发放时间。它不是未来 T3 所需的最终业务回执。

Hash 使用递归按键排序的 JSON 编码并保留整数精度；大于 2^53 的身份值有独立回归。三个固定向量分别锁定生产 evidence、qualification 和 invalidation 编码；生产事实采用 lowerCamel 字段 schema，不能把 Skeleton witness 的 Go struct 序列化直接当成生产编码。Hash 与 `CORE_COMPATIBILITY_COLLECTION` 来源字符串只用于内容完整性校验，不构成采集器认证。

Core `Service` 只接受 descriptor ID，再从应用装配的可信 `DescriptorProvider` 取得描述；采集时先读持久化描述，将完整身份交给 `IndependentCollector`，验证返回事实一致后自设来源和计算证据 hash。没有默认 provider/collector，也没有 HTTP、GPT MCP 或本地 MCP 的资格写入口。提供者缺失、采集错误、不兼容或事实不匹配均拒绝写入。

## 验证证据

测试采集器仅验证接口边界，不代表真实运行平台完成了兼容性测量。已完成的检查包括：

- descriptor / evidence 注册、资格发放、替换、两种来源状态的失效、连续两次冷重启和精确请求重放；完整快照证明重放无写入。
- 请求改变资格 ID、lineage、descriptor、generation、policy 或 evidence 均拒绝；跨操作复用请求及错误前驱拒绝；无当前资格时也不能重用旧代次。
- 同请求及不同请求并发竞争：只有一个新代次、一个 CURRENT，精确请求竞争者读取同一结果。
- 不可变 SQL 字段、删除、非法状态边、无生命周期写入的 lineage 推进均拒绝。
- 新资格 INSERT 前失败时，旧资格已执行的 SUPERSEDED 更新随事务完整回滚。
- 半完成 SUPERSEDE 和只插入 INVALIDATE request 的事务，必须在 COMMIT 被延迟复合外键拒绝。临时 overlay 撤回外键修复时，两例确实接受错误提交；没有用缺列或提前失败伪造负向证据。
- 真实 Store 在发放及替换的新资格插入后注入未应用失效请求，使 COMMIT 失败；平台与业务 13 表快照保持不变，移除故障后同请求可成功重试。临时 overlay 仅将四个写入口恢复普通事务时，两例确实出现残留写入；新连接清理路径通过。
- 过期 epoch、不存在的 Run 和取消的 context 拒绝写入，随后合法写入仍成功。
- 真实 v96 schema 上已有 Project / Repository / Module / Session / FeatureArea / Task 数据，经 v97 升级及再次冷打开，所有既有业务表快照不变；迁移不推导任何平台资格。原 v95→v96 历史用例同步适配 v97，仍单独证明 v96 实际执行。
- 可信构造依赖缺失、描述缺失或损坏、collector 错误、事实不匹配、不兼容及无产物摘要均拒绝；规范编码固定向量、嵌套键排序、整数精度及非法 UTF-8 身份检查通过。

所有 Go 命令使用 `GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache`。最终完整包回归共 **1086 个测试节点通过、零跳过、零失败**：

| 包 | 通过节点 | 耗时（秒） |
| --- | ---: | ---: |
| `internal/storage/sqlite` | 1060 | 128.701 |
| `internal/runtimeplatform` | 26 | 0.662 |

完整 SQLite 编译运行启动后，另补了模型对非法 UTF-8 字符串的拒绝，避免 JSON 替换字符导致不同原始身份得到相同编码。该最终改动经上述 runtimeplatform 完整单测及全部平台定向回归验证：**66 节点通过、零跳过、零失败，3.386 秒**；与完整 SQLite 结果重叠，不重复累加。

首轮完整 SQLite 回归暴露了两项延迟 COMMIT 清理失败，修复后完整重跑得到上表结果。独立复核已确认当前持久化范围没有剩余阻断问题。最终两包 `go vet`、本批 10 个 Go 文件格式和 `git diff --check` 均通过。

冻结证明：470 个 Skeleton 文件的 SHA256 与本批入口完全一致；无 staged / unstaged / untracked Skeleton 变化；freeze commit 是 HEAD 祖先，HEAD Skeleton tree OID 精确匹配。`make -C skeleton generate-check production-parity` 通过；其中 production parity 仅检查 Task.phase 词汇，不能作为完整运行流程一致性证明。未重复上一批已通过、源码未变的完整 Skeleton 长测。

与本批入口哈希清单相比，共 11 个文件新增或修改，无删除；变更限于新平台基础、v97 注册、相关测试及本文档。既有其他修改保持原样。本批没有重新运行未受接口变化影响的 HTTP / MCP 全包长测。

主要复现命令：

```sh
GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache go test ./internal/runtimeplatform -count=1 -json
GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache go test ./internal/storage/sqlite -count=1 -json -timeout=30m
GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache go test ./internal/storage/sqlite -run '^TestRuntimePlatform' -count=1 -json
GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache go vet ./internal/runtimeplatform ./internal/storage/sqlite
GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache make -C skeleton generate-check production-parity
git diff --check
```

本批运行日志及基线清单位于 `/private/tmp/devbridge-approve-start-u8c8xek6`；本文保留可持续审阅的结论和命令。生产修改保留在工作区，未提交。

## 后续边界

本批没有实现真实产物解析器和独立兼容性测量器。后续须为实际运行平台定义并实现测量策略，持久化可核验采集产物，再将可信提供者装配到 Core。不能用测试 fixture 的 `measuredCompatibility=true` 发放生产资格。

其后还须实现 formal approved selection → revision binding 的来源闭合，再重构 `APPROVE_AND_START`：T1 只提交新 revision 并保持 PREPARING；T2 原子提交 admission 与冻结 Plan；T3 原子提交 preparation authority、最终 receipt 和 READY。三阶段均不得创建 Run 或 dispatch，最终回执重放优先于部分进度及新鲜资格检查。现有 APPROVE_AND_START 行为在本批保持原状，完整三阶段恢复仍未实现。

当前冻结契约对上述事实和边界足够明确；本批未修改 Skeleton，也未导入其 witness evaluator 充当生产引擎。
