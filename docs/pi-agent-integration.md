# PI Agent 执行端适配

## 概述

本实现为 devbridge 项目添加了 PI Agent 执行后端适配器，使现有的执行管道可以调度 PI Agent 作为 Codex 的替代执行引擎。

## 架构设计

### 核心组件

1. **RPC 客户端** (`internal/piagent/client.go`)
   - 通过 JSONL over stdin/stdout 与 PI Agent 子进程通信
   - 实现命令-响应关联和异步事件流
   - 支持 `prompt`、`get_state`、`abort` 等 RPC 命令

2. **会话适配器** (`internal/piagent/session.go`)
   - 实现 `execution.CodexSession` 接口
   - 将 PI Agent 的会话模型映射到 devbridge 的线程/轮次模型
   - 支持结构化输出（通过追加 schema 指令到 prompt）

3. **工厂适配器** (`internal/piagent/session.go`)
   - 实现 `execution.CodexFactory` 接口
   - 支持模型选择、推理级别等配置
   - 兼容现有的 `modelAwareCodexFactory` 和 `operationAwareCodexFactory` 接口

4. **可执行文件解析** (`internal/piagent/resolve.go`)
   - 类似 `codexlaunch` 的确定性路径解析
   - 支持 NVM、Volta、npm 全局安装等常见位置

### 关键映射

| Codex 概念 | PI Agent 等价物 | 说明 |
|-----------|----------------|------|
| Thread | Session | PI Agent 每个会话即一个线程 |
| Turn | Prompt | 发送 prompt 并等待 `agent_settled` |
| RuntimeBinding | CWD | 工作目录通过 `cmd.Dir` 设置 |
| Approval | N/A | PI Agent 暂不支持审批流程 |
| Structured Output | Schema 指令 | 追加到 prompt 的 JSON schema 说明 |

## 使用示例

### 基本使用

```go
import (
    "context"
    "github.com/lazyun/devbridge/internal/piagent"
)

// 创建工厂
factory := &piagent.Factory{
    Model: "gpt-5.5",
    ThinkingLevel: "medium",
}

// 启动会话
session, err := factory.Start(ctx, config)
if err != nil {
    return err
}
defer session.Close()

// 启动线程（PI Agent 中为 no-op）
threadID, err := session.StartThread(ctx, "/path/to/workspace")

// 执行轮次
result, err := session.StartTurn(ctx, threadID, "实现用户登录功能")
if err != nil {
    return err
}

fmt.Printf("输出: %s\n", result.Message)
```

### 集成到执行管道

在 `internal/execution/coordinator.go` 中，可以通过以下方式启用 PI Agent：

```go
// 根据配置选择执行后端
var codexFactory execution.CodexFactory
if usePiAgent {
    codexFactory = &piagent.Factory{
        PiPath: piagent.Resolve("pi"),
        Model: "gpt-5.5",
    }
} else {
    codexFactory = existingCodexFactory
}

coordinator := &execution.Coordinator{
    Codex: codexFactory,
    // ... 其他依赖
}
```

## 限制和注意事项

### 当前限制

1. **无审批流程**: PI Agent 不支持 Codex 的审批请求/决策流程
2. **无会话恢复**: 每次启动都是新会话，不支持跨进程恢复
3. **结构化输出**: 通过追加 schema 指令实现，非原生支持
4. **单线程模型**: 每个会话只能顺序执行 prompt

### 认证要求

PI Agent 需要预先配置认证信息（API key 或 OAuth）。如果未配置，prompt 可能返回空输出。

### 性能考虑

- 启动延迟：PI Agent 子进程启动约需 100-200ms
- 内存占用：每个会话约 50-100MB
- 并发限制：建议同时运行的会话数不超过 CPU 核心数

## 测试

运行测试：

```bash
go test ./internal/piagent/ -v
```

测试覆盖：
- ✅ 可执行文件解析
- ✅ 客户端启动和关闭
- ✅ RPC 命令发送和响应接收
- ✅ 事件流处理
- ✅ 错误处理和超时

## 后续工作

### 短期

1. **添加 ExecutorKind**: 在 `internal/review/report.go` 中添加 `ExecutorPiAgent`
2. **配置集成**: 在 `codexconfig` 中添加 PI Agent 配置选项
3. **UI 支持**: 在确认卡片中添加 PI Agent 选项

### 中期

1. **审批流程**: 实现 PI Agent 的审批请求/决策适配
2. **会话持久化**: 支持跨进程会话恢复
3. **多模型支持**: 支持在会话中切换模型

### 长期

1. **性能优化**: 减少启动延迟，优化内存占用
2. **功能对齐**: 完整对齐 Codex 的所有功能
3. **监控和诊断**: 添加详细的执行跟踪和性能指标

## 故障排除

### 常见问题

1. **"pi executable not found"**
   - 确保 PI Agent 已安装：`npm install -g @earendil-works/pi-coding-agent`
   - 或手动指定路径：`Factory{PiPath: "/path/to/pi"}`

2. **"PI Agent returned empty output"**
   - 检查认证配置：`pi auth status`
   - 查看 stderr 输出获取详细错误信息

3. **"pi agent subprocess exited"**
   - 检查 PI Agent 版本是否支持 RPC 模式
   - 查看 stderr 输出获取启动错误

### 调试技巧

启用详细日志：

```go
client, err := piagent.Start(ctx, cfg)
if err != nil {
    log.Printf("Stderr: %s", client.Stderr())
}
```

## 参考

- [PI Agent RPC 文档](/Users/phoenix/.nvm/versions/node/v23.11.1/lib/node_modules/@earendil-works/pi-coding-agent/docs/rpc.md)
- [PI Agent SDK 文档](/Users/phoenix/.nvm/versions/node/v23.11.1/lib/node_modules/@earendil-works/pi-coding-agent/docs/sdk.md)
- [devbridge 执行架构](internal/execution/coordinator.go)
