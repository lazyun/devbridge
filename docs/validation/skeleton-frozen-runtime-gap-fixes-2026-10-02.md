# 运行约束缺陷修复验收（2026-10-02）

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-3ae21532764442d8)。

本轮处理上一轮审核确认的 5 项缺陷。`skeleton/**` 仅作为约束来源，不修改、不运行其整包测试。产品改动延续当前工作区，尚未提交。

## 修复结果

1. **同制品、多后端资格冲突**：精确 descriptor、角色和兼容策略相同时，第二个后端显式授权复用 CURRENT 资格，不再使第一个后端失效。v105 将原映射保存为不可变请求回执，新增显式映射指针及版本检查；旧映射失效后必须由新请求推进。迁移保留原 payload/hash，原请求和已成立修订绑定始终读取原资格，不根据最新资格替换历史事实。
2. **REQUEST_CHANGES 与 UNKNOWN 恢复**：v2 Coding 修改定义须经过补充 Review 和正式审批，Prepare、Options、Task Actions 提前说明该入口不可执行。历史 UNKNOWN 仅在旧 writer 已被新 epoch 隔离、原 intent/提交/步骤/目标身份一致、所有必需业务结果均不存在时，纯读投影为 REJECTED/NO_EFFECT_PROVEN；原 CONSUMED 提交记录不变。同 worker 或证据不足时保持 UNKNOWN。Core 公共投影、MCP schema 和卡片同步接受严格的 REJECTED_NO_EFFECT 结果，保持不可重复提交；同时补齐正式审批 PREPARATION_READY 的“已准备、未启动”展示。
3. **Web NO_START 启动入口**：未正式绑定的定义不再提供必然失败的 operator 启动操作，提示使用授权 AI Session 的 START_APPROVED 确认。原命令回执先读，已正式绑定候选的 operator 启动路径保留。
4. **测量与实际启动不一致**：资格验证返回捕获并可重验的 native 启动身份，实际启动直接使用该路径，不重新解析 npm wrapper、Settings 或 PATH。continuity 重连从不可变 Execution 恢复精确绑定，在连接前检查资格和原进程路径，恢复执行时再次验证。普通非 qualified 协议路径保持原行为。
5. **重试/重新验证可用性失真**：只读资格检查复用写入端的精确绑定检查，校验 10 个运行字段、原资格生命周期、冻结配置，以及后端是否启用、指纹是否仍匹配。INVALIDATED、缺失绑定、字段不匹配、停用模型或后端提前不可用；SUPERSEDED 仍允许已成立绑定。同修订重试锁定运行配置和定义，修改指令或范围须重新 Review。

## 定向验证

证据目录：`build/constraint-gap-fixes-2026-10-02/`。

| 验证 | 结果 / 证据 |
| --- | --- |
| SQLite v104→v105 原回执迁移、映射版本冲突、事务回滚、冷重放 | 3 项通过，`backend-mapping-storage.log` |
| 安装的真实 native：同制品双后端、显式更新、原请求冷重放 | 通过，`backend-mapping-native.log` |
| 恶意 npm 形 wrapper 与实际 native 分歧 | 修复后通过；撤掉最终启动分支的临时 overlay 失败，分别见 `qualified-launch-green.log`、`qualified-launch-red.log` |
| 原绑定 CURRENT/SUPERSEDED/INVALIDATED、冻结模型停用、后端停用/指纹改变、公开重新验证入口 | 3 项通过，`continuation-read-eligibility-final.log`；拒绝 Prepare 和只读投影前后持久化快照一致 |
| 旧 UNKNOWN 冷恢复、真实运行取消/停止后修改、重试公开入口 | 通过，`controlapi-runtime-boundaries-final.log`；保留同 worker UNKNOWN 与原 CONSUMED 事实 |
| Web NO_START 禁用边界及已正式绑定候选可用 | 通过，`controlapi-runtime-boundaries-final.log`；该日志共 8 个顶层用例通过 |
| 产品包编译检查 | `go test ./cmd/... ./internal/... -run '^$'` 通过，`production-compile-final.log` |
| 真实 native 正式审批与 NO_START→显式采用 | 2 项通过，`native-formal-final.log`；更新后端指针后原 q1 绑定和回执仍保持原值 |
| 最终源码打包后的公开生命周期 | 通过，`packaged-native-source-final.log`；NO_START、两个成功运行、ACCEPT、Task 提交、FeatureArea 合并、冷重放 |
| continuity 恢复前拒绝失效资格/不同 native | 通过，`qualified-recovery-green.log`；撤掉恢复检查的 overlay 为失败，见 `qualified-recovery-red.log` |
| 受影响产品包 go vet | 通过，`affected-go-vet-final.log` |
| skeleton 冻结校验 | 原 470 个文件全部一致，无新增文件，`skeleton-read-only-verification.json` |
| 真实旧确认冷恢复经 MCP 投影为卡片 | 通过，`retired-card-capture.log`；使用从创建起即匹配的真实 caller scope 和原始提交 DTO |
| 确认卡片浏览器端契约 | 62 项通过、0 失败，`confirmation-ui-final.log`；包括无效果拒绝、不完整证明拒绝和准备就绪展示 |
| 共享确认结果及 Card 服务端投影 | 通过，`confirmation-outcomes.log`、`confirmation-card-projection.log` |

协议 subprocess fixture 只验证产品协议和状态机，不作为 native 兼容证据。真实 native 用例使用隔离目录和本机回环服务，不调用线上模型服务。continuity 恢复新增测试通过公开恢复入口和只读测试端口验证，不宣称真实 0.159.2 的 active-turn rejoin 已验证；既有 continuity 的版本兼容策略未在本轮更改。

## 验收边界

本轮为上述缺陷的定向验收，并非项目所有测试通过的声明。`REQUEST_CHANGES` 不会被扩展为新正式 source 发行者；未证明无业务效果的 UNKNOWN 不会被改写为失败。历史回执读取与新运行授权分别遵循原有约束。
