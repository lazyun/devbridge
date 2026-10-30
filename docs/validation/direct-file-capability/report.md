# DIRECT_FILE_CAPABILITY_V0_1_CLOSURE

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](../README.md)。 本报告整理前的原文及同期证据保存在[批次归档](../archive/direct-file-capability.md)中。

> 后续集成授权：用户要求合并到开发分支并清理工作分支。核验目标为 `dev_20260908_1913`，当前已在该分支，本次没有独立工作分支；采用直接提交。下列未提交/Git 状态记录保留为实施验收时的快照。

已实施，工作区保留供人工检查。无 commit / push / tag / release。

| # | 交付项 | 结果 |
| --- | --- | --- |
| 1 | Starting branch / HEAD | `dev_20260908_1913` / `dc713fdb69e72729fc661fabec0c5a9459605032`；fresh 核验工作区干净，完全匹配 baseline。 |
| 2 | Capability contract | 默认 CODEX，DIRECT_FILE disabled；只有 Upper LLM 在用户明确授权后调用 enable 才启用。Core 不解析自然语言。八个工具，所有文件工具包括 read/list 统一受控。 |
| 3 | Lifecycle | `aiSessionId + featureAreaId + workflowId` 绑定 Project / Repository / Workspace / Worktree；连续调用共享临时授权；最终调用 `workflowComplete=true` 在成功或失败后自动关闭；显式 disable 用于取消/提前完成。重复 enable 拒绝；同会话不同 workflow 调用拒绝并撤销旧授权；重启/关闭/Retire 后不可复用。 |
| 4 | Codex mutual exclusion | 复用 SQLite `beginWrite` writer/admission 边界；`createRunAttempt` 在插入 Run 前检查，事务回滚不留下 Run/claim/attempt 变化；BeginRun/Reset 再检查。enable 拒绝 active Run、claim、未知 backend boundary、pending Git finalization/submission。独立 FeatureArea B 可启动 writable Run。 |
| 5 | Canonical worktree | aiSession Project → FeatureArea current_workspace_id → ready、nonquarantined Workspace → READY Worktree → 同 Project Repository。无调用方 root 参数；固定 Workspace/Worktree ID、canonical path、目录文件身份；拒绝 repository checkout、development ref worktree 与其他注册 root 的别名。 |
| 6 | Path / symlink safety | 统一 relative resolver，路径清理与逃逸检查，拒绝绝对路径、越界 traversal、`.git` 大小写别名以及路径中的全部 symlink。`os.Root` 限制访问；目录替换/指向别处的 symlink 导致撤权与拒绝。list 只报告 symlink name/type，不跟随。 |
| 7 | file_list | exact relative directory；`.` 表示 worktree root；非递归，最多 256 entries，name/type 与 truncated；空目录成功。 |
| 8 | file_read | regular file，最多 256 KiB，返回完整 content、contentEncoding、size；保留空内容、CRLF、前后空白、Unicode。非 UTF-8 使用 base64，避免 JSON 丢失字节；FIFO/特殊文件拒绝，不阻塞。 |
| 9 | file_create | whole-file only；不存在目标才创建；已有目标 FILE_ALREADY_EXISTS，不覆盖。 |
| 10 | Missing parents | create 与 move destination 自动创建缺失父目录，始终经相同 root/resolver；不暴露 mkdir。 |
| 11 | file_replace | 仅已存在 regular file；missing / directory 分别拒绝；整文件临时写入后 rename，保留权限，失败不损坏原文件。 |
| 12 | file_delete | 仅单个 regular file；不存在拒绝；使用 file-only unlink，不能删除目录，不递归，不清理父目录。 |
| 13 | file_move | `path` 为 source、`destination` 为目标，二者均 relative；source regular，destination 不存在；Darwin RENAME_EXCL / Linux RENAME_NOREPLACE；跨文件系统/不支持时明确失败，无 copy+delete。 |
| 14 | Atomic publication | same-directory exclusive temp → complete write → Sync → Close；create 用 hard-link 原子 no-clobber publish，replace 用 rename；清理临时文件。并发 create 只有一次成功；publish failure 保留原件且无 temp 残留。 |
| 15 | Error contract | typed `directfile.Error{Code,Message}` → controlapi.Error，MCP `isError + structuredContent.code/message`；包含任务要求的最小错误词汇。destination 已存在统一 FILE_ALREADY_EXISTS；DESTINATION_ALREADY_EXISTS 保留为已定义别名词汇。 |
| 16 | Limits | read 256 KiB、decoded create/replace 256 KiB、list 256、relative path 4096 bytes、scope ID 128 bytes；base64 schema maxLength 349528；现有 MCP request body 上限继续生效。 |
| 17 | Git boundary | file service 无 Git/process import，不执行 Git 命令。真实临时 Git repo 集成比较 HEAD、refs、index：全部 byte-identical，仅 worktree bytes 变化。Task auto commit / FeatureArea submission 保留原 authority，并互斥 admission。 |
| 18 | Prompt Neutrality | Codex prompt construction/transport、final_codex_prompt 与 byte-identity 生产代码零改动；现有 execution/controlapi/Review 回归通过。 |
| 19 | Schema / storage | schema v45 不变；无 migration、无新表、无 durable capability/lock/lease、无 dependency 变更。授权仅进程内状态，DB close 清理句柄。 |
| 20 | Modified files | 见 [完整清单](../archive/direct-file-capability.md#file-0dd6f32949c13c8c)。实现集中于 directfile、SQLite admission、controlapi/bootstrap、MCP/shared presentation 与只读 WebUI badge；另修正两个已有 controlplane Review fixture 缺少必填 ReviewDepth 的基线问题。 |
| 21 | Go tests | 15 个受影响/回归 packages 通过（retirement 无测试）；focused DirectFile integration 通过。见 [Go](../archive/direct-file-capability.md#file-06ed182159b7faf0)、[MCP](../archive/direct-file-capability.md#file-4a648a7836a2f548)、[focused integration](../archive/direct-file-capability.md#file-e791fb06f9b2321f)。 |
| 22 | Race tests | Direct File 全包 race（含真实临时 Git test）、SQLite/Facade targeted race、原 same-area/different-area concurrency 回归通过。见 [file race](../archive/direct-file-capability.md#file-b02b85619e8e3bb3)、[admission race](../archive/direct-file-capability.md#file-6c60bf562d5aca0c)。使用独立临时 GOCACHE 避开本机旧缓存的 runtime/race 构建问题。 |
| 23 | Node tests | Product WebUI + MCP Card 共 146 tests 全通过，含状态 badge；无失败/跳过。见 [Node](../archive/direct-file-capability.md#file-f4060b340cc048c4)。 |
| 24 | Static audit | 禁止的 mutation/patch/edit/append/shell/CAS/lease API 命中 0；public Scope/Input 无 absolute root；schema/dependencies/prompt changes 为 0。见 [static audit](../archive/direct-file-capability.md#file-b0f3a740b4bd1eeb)。 |
| 25 | go vet | directfile、sqlite、controlapi、controlplane、workflowpresentation、mcpserver、localmcp、bridgehttp 均通过。见 [vet](../archive/direct-file-capability.md#file-2632bab646a31936)、[final vet](../archive/direct-file-capability.md#file-e473667bdab0ba88)。 |
| 26 | git diff --check | 通过，见 [diff check](../archive/direct-file-capability.md#file-089c8e9344085f07)。 |
| 27 | Final git status | 原 branch / HEAD，改动全部未暂存，保留人工检查。见 [git status](../archive/direct-file-capability.md#file-be14a57fcfd14b23)。 |
| 28 | SCOPE_CONFLICT | 无。未引入 Mutation Core、CAS、持久锁、heartbeat、长期文件模式或第三方 filesystem library。 |
| 29 | FAST / DEEP | 保持；Task-owned v45、Review 1:1 Run、Task-first UI、same-area serialization、不同 area 并行、Retire、auto commit / submission 的整包回归通过。 |
| 30 | Production recall host | 仍为 `HOST_INTEGRATION_PENDING`；未执行 host integration、用户设备操作、真实 Codex、live DB 或外网访问。测试只使用临时目录/数据库、fake backend 与 loopback。 |
| 31 | 发布边界 | 明确未 commit、未 push、未 tag、未 release，未修改其他 worktree。 |

能力关闭的边界是显式 scoped workflow contract；无状态 MCP 无法观测 Upper LLM 的自然语言回合结束。Upper LLM 必须在最终文件调用标记 completion，或在完成/取消时 disable，并为无关新需求使用新的 workflowId。Core 不以 timer、连接断开或自然语言猜测替代授权。

v0.1 文件操作支持 Darwin / Linux，其他平台 fail closed。外部用户/IDE 并发修改属于已声明边界；本轮未扩展为文件事务/CAS 系统。
