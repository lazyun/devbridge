# 任务级命令授权

状态：2026-10-24，最终 `.69` 已部署。任务面板 v38、确认单 v11；schema 仍为 132。

用户对完整方案作一次正式确认。Core 将工作范围和命令权限绑定到准确 Task、不可变定义版本、Execution、Run 与 RuntimeWorkset。实施、Codex 审查、验证使用各自的原生沙箱；范围内的命令无需逐条批准。该机制不代替工程验收、语义问题的用户回答或最终合入确认。

## 方案与授权

`workSpec.commandPermissions` 是可选的不可变方案字段：

```json
{
  "networkAccess": false,
  "verificationOutputs": ["artifacts"]
}
```

省略字段保持历史定义的编码与哈希不变，默认关闭网络。启用网络必须写入完整方案并由用户正式确认。确认卡片展示网络权限和额外验证产物目录。SQLite 派发守卫从准确 Execution 关联的不可变定义核对网络授权，不能从模型请求或当前可变 Task 推断同意。

扩大工程范围、开启此前未授权的网络，必须沿原任务的正式修订资格重新准备并确认。不会安装永久命令前缀规则，也不会更改用户个人 Codex Desktop/CLI 配置。独立的 GPT 文件审查继续走原有文件授权，不被描述成命令授权。

## 运行环境

Core 在模型派发前创建私有的 `.task-runtime/run-<摘要>/`，持久化权限清单，再绑定 RuntimeWorkset。临时文件、Go 构建/npm/Python/XDG 缓存定向到独立 scratch。实施仅开放冻结的仓库或 PATHS 写入范围；`.git`、`.codex`、`.agents` 元数据保持只读。

Codex 只读审查和验证使用独立 Git 副本，保留实施中的未提交、未跟踪文件和已删除状态。副本没有共享索引、硬链接或指回原仓库的 Git 元数据；外部、失效符号链接及特殊文件拒绝复制。原工作树与副本源码均只读。

副本仅允许写入明确产物目录和 scratch。Core 识别常见构建目录（例如 dist、build、node_modules、target、coverage）；它们只有在 Git 忽略、没有跟踪或非忽略源码且路径不经过符号链接时才可写。方案中的 `verificationOutputs` 使用同样守卫，不安全的显式请求直接拒绝。另外识别 `tsconfig.tsbuildinfo`、`tsconfig.app.tsbuildinfo`、`tsconfig.node.tsbuildinfo` 及 `frontend/` 下的同名缓存文件；仅在 Git 忽略、未跟踪且不是符号链接时授予准确文件写入，不开放其父源码目录。只给副本写权限，不会对原工作树添加权限。多仓中的显式目录必须在每个副本中均符合条件。

权限使用原生具名 profile，通过 Bridge 子进程的启动参数配置；thread/start、thread/resume、turn/start 均绑定同一 profile，并设置 `approvalPolicy=never`。不能混用 legacy sandbox 字段。Core 在首个模型 Turn 前检查实际 activePermissionProfile；无法观察到准确 profile 时停止派发。hooks、子代理和 web_search 关闭；既有插件与继承 MCP 隔离守卫保持生效。

命令超过边界时原生沙箱拒绝。若原生运行时仍发出扩权审批，Core 记录系统 REJECTED/JSONRPC_ERROR 并返回错误，不伪造用户同意，不留下永久等待审批。工程代理应记录该检查 BLOCKED，并继续独立检查；用户语义问题 `CODEX_REQUEST` 仍须真实回答。

## 证据、恢复和兼容

恢复状态读取使用与原进程绑定的独立诊断 profile：即使原实施允许写入或联网，诊断也只允许读取源码、写 scratch，关闭网络。Core 将诊断限定为已有正式恢复协议创建的只读子 Thread；缺少准确来源绑定时拒绝连接。不会把原实施权限带入诊断 Turn。

权限清单有独立内容哈希，不改变历史 RuntimeWorkset 的工程授权哈希。检查点和原生进程绑定权限哈希；恢复前核对准确 Run、版本、工作树身份、副本身份、CWD、目录集合与权限，拒绝扩大或替换。已经持久化的历史 Run 不热升级权限，不为加载配置重启活动 Turn。

命令证据记录实际副本 CWD 与权限哈希，Git 源码证据仍绑定原工作树。只有准确冻结的副本映射可成为有效验证来源。Core 在结束时额外核对副本源码摘要、Git 快照及原工作树状态；运行时源码只读限制防止“改代码、测通过、再恢复”绕过结束检查。构建副本的修改不会成为交付。

该边界沿用现有受信任工程执行器模型，不声称防御恶意原生二进制或操作系统级攻击。原生无模型测试已验证实际文件与网络控制；付费模型 Turn 后的真实原生恢复未作为本次验证证据，恢复协议与持久化由 Go 回归覆盖。

验证记录见 [任务级命令权限验证](../validation/task-command-permissions-2026-10-24.md)。

## 项目级可信执行授权

项目级可信执行授权是本地操作者显式、持久的项目设置，只能在本地 Console（`GET/PUT /v1/projects/{id}/execution-authorization`）修改，工程代理的 MCP/GPT 工具目录不提供项目权限写入工具；本地 HTTP 控制面属于受信任宿主边界。授权是单实体、仅追加修订历史（`project_execution_authorization_revisions`）加头指针（`project_execution_authorizations.head_revision`，迁移 v135）；每次保存以 expectedRevision CAS 生成新修订，历史修订不可变。项目默认未授权；没有授权记录时行为与既有逐路径模型完全一致。

每次 Run 在 prepareTaskPermissions 时读取一次授权并冻结为 TaskPermissions 的 `trustedExecution` 快照（projectId + authorizationRevision + trusted + networkFromAuthorization，omitempty，历史清单 JSON 字节与哈希不变）。已冻结的 Run 在恢复/重放时校验其冻结修订的持久修订行，不读当前头：撤销授权不影响已开始的 Run，只影响之后的新 Run。首次持久绑定在同一 SQLite 事务内额外要求冻结修订仍是当前头，防止 prepare→撤销→bind 竞争。

可信实施/验证的沙盒放宽为：成员工作树根 + scratch + 通用临时目录（`:tmpdir=write`、`:slash_tmp=write`，`:root` 仍只读，`.git`/`.codex`/`.agents` 强制只读），免除逐路径/逐命令审批；Git 提交等元数据操作仍由宿主负责。网络授权采用 OR 语义：冻结任务定义的网络授权，或项目网络授权（`NetworkFromAuthorization` 且持久修订行 network=1、project 匹配）。SQL 守卫始终回读数据库真实来源，不信任 JSON 清单。SCOPE_REVIEW 与诊断运行不接受可信快照，保持只读、禁网。

可信验证不复制源码：直接在原任务工作树运行，成员根可写（允许写被忽略的构建输出），保留独立只读角色语义。结束检查复用现有快照机制：结束时对每个成员根重新取 Git 快照（`git add -A` 于临时索引，遵循 .gitignore，因此被忽略的构建输出不触发失效），与冻结的 StartSnapshot 比对；tracked 或非忽略 untracked 文件的新增/删除/修改都会使运行失效（`VERIFICATION_MUTATED_WORKTREE`），PATHS 范围仍由既有 GitEvidence 检查覆盖。违规不自动撤销，只记录失效并保留证据。可信模式不声称物理阻止源码写入——结束校验是失效与证据机制，不是物理沙盒边界。


两个开关独立：只授予项目联网时仍使用原文件沙箱和验证副本；只授予可信执行时不新增联网能力。快照中的网络位完整记录该项目修订的授权，即使任务定义也已经单独授予网络。可信且显式联网的 Go 运行将 GOMODCACHE 指向系统临时目录中的项目专属共享缓存，避免依赖下载写入沙箱外的用户 GOPATH；离线、仅联网和诊断不改变已有模块缓存位置。

可信验证的清单必须不含 Copies；异常副本、文件系统根写入、跨项目快照被拒绝。命令验证证据在原工作目录下仍须携带准确 TaskPermissionHash，缺失或错误的权限哈希、跨 Run/仓库/目录的命令不能计为当前检查证据。
