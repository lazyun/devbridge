# Dev Bridge

**把 AI 编程从一次对话，组织成可审查、可验证、可追溯的工程流程。**

Dev Bridge 是运行在本机的 AI 开发工作流协调工具。它连接 ChatGPT 或本地 MCP 客户端与 Coding Agent，把项目上下文、需求审查、用户授权、代码实现、验证和 Git 交付保存在同一个持久化流程中。

你选择项目、提出目标并确认关键决策；Dev Bridge 管理独立的 Git 工作区、执行状态、证据和历史。通过 Desktop 和 Web Console，可以查看任务进度、运行会话、文件变化及待处理的决策。

> 当前阶段：**Alpha（开发预览）**，构建版本为 `0.1.0-dev`。欢迎本地试用和反馈；功能、接口和数据格式仍可能调整。本文以仓库当前源码和脚本为准，历史里程碑见[开发记录](docs/development-history.md)。

[快速开始](#快速开始) · [接入 MCP](#接入-mcp) · [开发与测试](#开发与测试) · [用户指南](docs/user-guide/README.md) · [接口参考](docs/interfaces.md)

## 主要能力

- **持久化工程上下文**：以 Project、FeatureArea 和 Task 组织项目与需求，保留 Review、Execution、Run 和决策历史。
- **审查与授权**：创建任务时由用户选择 AI 文件审查、Codex 审查或跳过审查；跳过后仍须确认实施方案，执行时显式选择后端、模型与推理强度。
- **独立 Git 工作区**：在 Bridge 管理的 worktree 中执行任务，同一 FeatureArea 可以延续工作区和 Agent 对话；不同 FeatureArea 可并行工作。
- **执行与分析分离**：Codex 使用自然语言回复，Core 记录运行是否成功、命令退出码和文件变化；GPT 在审查或验证结束后独立分析，再由用户审批或验收。
- **Git 交付流程**：验收后生成交付提交，再显式确认 FeatureArea 合并；项目可登记多个仓库，操作使用明确的仓库目标集合。
- **多种交互入口**：提供 Wails Desktop、Web Console、面向 ChatGPT 的 GPT MCP adapter 和独立的 Local MCP adapter。
- **运行观察与恢复**：查看会话、Codex 原生运行状态、日志、权限请求与可用的 token 用量；运行中可向当前回合追加说明，中断后保留历史，并根据当前状态提供恢复或取消路径。

## 工作流程

```text
选择 Project 与 Development Branch
                  ↓
       FeatureArea → Task → Review
                              ↓
                         用户批准
                              ↓
           Execution：Implementation → Verification
                                               ↓
                                           用户验收
                                               ↓
                                Git Commit → FeatureArea Merge
```

- **Project**：逻辑项目边界，可以登记多个本地 Git 仓库。
- **FeatureArea**：持续推进的一组相关工作，拥有工作区与 Agent 对话。
- **Task**：具体工程目标，可经历多轮审查、修改和交付。
- **Execution / Run**：一次交付尝试及其中的实现、验证等运行记录。

- **Document**：用户明确要求保存的项目知识，可关联 FeatureArea；普通对话不会自动保存为文档。

Codex 运行成功只表示本次执行完成。新运行不要求 Codex 输出 JSON；审查和验证结束后，由 GPT 读取原始输出与不可变执行证据并提交正式分析。

批准 Review 会冻结当前任务定义；“仅批准”后仍需显式启动实现。选择“不审查”则进入单独的实施方案确认，不会在创建任务时执行代码。验收和合并也是独立决策。完整规则见[开发工作流](docs/user-guide/workflow.md)。

## 系统结构

```text
ChatGPT ── OpenAI Tunnel ── devbridge-mcp ────────┐
                                                │
本地 MCP 客户端 ── devbridge-local-mcp ────────────┤
                                                ▼
Desktop / Web Console ──────────────────── devbridge Core
                                                │
                              ┌─────────────────┼─────────────────┐
                              ▼                 ▼                 ▼
                         SQLite 状态       Git worktrees    Coding Agent
                                                              (Codex)
```

Core 是唯一持有 DataRoot 写锁的进程，负责持久化状态和工作流。两个 MCP adapter 通过本机 HTTP 代理到同一个 Core，可以同时运行。Desktop 负责本地服务管理、设置和原生界面，并复用 Core 的 Web Console。

技术栈：Go 1.25、SQLite（纯 Go 驱动）、MCP Go SDK、Wails v2、JavaScript / Vite、Codex App Server。

## 快速开始

### 环境要求

| 依赖 | 用途 |
| --- | --- |
| Go **1.25 或更新版本** | 构建 Core、MCP adapters 和 Desktop Go 模块 |
| Git | 管理项目仓库、worktree、提交与合并 |
| 可用的 Codex 可执行文件及其认证配置 | 执行真实 Coding Agent 工作，需支持 `codex app-server` |
| Node.js 与 npm | 构建和测试 Desktop 前端 |
| Wails CLI **v2.10.2** 及平台依赖 | 开发或打包 Desktop |

仅运行 Core 与 MCP 时无需安装 Wails 或构建 Desktop 前端。真实模型调用使用你配置的后端和账户；Dev Bridge 不提供模型服务。不同 Codex 版本的协议能力可能不同，请结合[兼容性与限制](#兼容性与限制)确认所用路径。

### 获取源码与构建

```sh
git clone https://github.com/lazyun/devbridge.git
cd devbridge

go mod download
go build -trimpath -o ./build/bin/ ./cmd/...
```

构建得到三个程序：

| 程序 | 职责 |
| --- | --- |
| `devbridge` | Core 服务、Web Console 和诊断入口 |
| `devbridge-mcp` | 面向 ChatGPT 的 MCP adapter |
| `devbridge-local-mcp` | 面向本地 MCP 客户端的 adapter |

以下终端命令以 macOS / Linux shell 为例，在仓库根目录执行。

### 方式一：Desktop

先按 [Wails v2 安装说明](https://wails.io/docs/gettingstarted/installation)准备对应平台依赖，然后安装仓库使用的 CLI 版本：

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2
```

确保 `wails` 在当前 shell 的 `PATH` 中，然后运行：

```sh
./build-desktop.sh
```

macOS 下的输出结构为：

```text
build/
├── Dev Bridge Desktop.app
└── bin/
    ├── devbridge
    ├── devbridge-mcp
    └── devbridge-local-mcp
```

```sh
open "build/Dev Bridge Desktop.app"
```

应用包与 `bin/` 需要保持上述相邻关系，搬移时一起搬移。非 macOS 构建的 Desktop 产物保留在 `desktop/build/bin/`；详细开发和运行配置见 [Desktop 文档](desktop/README.md)。

首次使用：

1. 在 Settings 中确认 DataRoot、Core/MCP 路径和 Codex 可执行文件，然后启动本地运行时。
2. 在 **Projects → Add Project** 中选择已有的本地 Git 仓库，确认项目名。
3. 在 **Settings → Coding Agent** 中读取模型目录，启用需要的模型并设置默认推理强度。目录可读不代表账户拥有对应模型权限。
4. 在接入的 AI 客户端中显式选择 Project，并为需要工作的仓库配置 Development Branch。
5. 提出目标，在 Task 卡片选择审查方式，确认实施方案与运行配置后执行，并在完成后验收和交付。

AI 文件审查由发起对话中的 AI 读取文件并提交报告，不会创建 Codex 后台会话；连续 5 分钟无 Bridge 可验证活动时会提示回到该对话继续。

普通 ChatGPT 对话继续支持上述流程。Work / Cloud 的 Task 主动通知是可选增强；本地会话页还可按准确 Run 读取 Codex 历史。适用条件与验收步骤见[Task 通知与 Codex 历史](docs/user-guide/task-events-and-codex-history.md)。

项目登记只保存项目与仓库绑定，不会自动启动 AI 工作。选择 Development Branch 只更新 Bridge 元数据，不会替你在原仓库执行 checkout。

### 方式二：直接启动 Core 与 Web Console

在一个终端中运行，使用独立于源码仓库的 DataRoot：

```sh
./build/bin/devbridge \
  -mode server \
  -data-root "$HOME/devbridge-data" \
  -listen 127.0.0.1:8081
```

看到 `DEVBRIDGE_READY` 后，打开 [Web Console](http://127.0.0.1:8081/ui/)。在另一个终端检查就绪状态：

```sh
curl --fail http://127.0.0.1:8081/readyz
```

服务模式必须传入 `-mode server`；省略时程序默认运行 `all`，会启动真实 Codex smoke。`-listen` 未指定时使用随机 loopback 端口，以上示例固定为 `8081` 便于接入。

## 接入 MCP

### 本地 MCP 客户端

保持 Core 运行，在另一个终端、同一仓库根目录启动 Local MCP adapter：

```sh
./build/bin/devbridge-local-mcp \
  -data-root "$HOME/devbridge-data" \
  -core-url http://127.0.0.1:8081 \
  -listen 127.0.0.1:8090
```

在支持 **Streamable HTTP** 的 MCP 客户端中添加服务地址：

```text
http://127.0.0.1:8090/mcp
```

例如，支持以下格式的客户端可以使用：

```yaml
mcpServers:
  devbridge:
    type: streamable-http
    url: http://127.0.0.1:8090/mcp
```

配置文件格式以具体客户端为准。可用 `curl --fail http://127.0.0.1:8090/readyz` 检查 adapter 就绪状态。客户端会话需要初始化，并显式选择 Project；详细工具调用和决策方式见 [Local MCP 指南](docs/user-guide/local-mcp.md)。

### ChatGPT / GPT adapter

GPT adapter 使用独立的工具目录和交互式确认组件：

```sh
./build/bin/devbridge-mcp \
  -data-root "$HOME/devbridge-data" \
  -core-url http://127.0.0.1:8081 \
  -listen 127.0.0.1:8080
```

MCP 地址为 `http://127.0.0.1:8080/mcp`。ChatGPT 接入还需要单独配置 OpenAI Tunnel 及相应的客户端连接；启动本地 adapter 只是其中一步。Desktop 提供 tunnel-client 路径、Tunnel ID 和凭据等设置，具体字段以当前界面为准。

所有 adapter 的 `-data-root` 必须与目标 Core 一致。它们不直接打开 SQLite，也不各自启动一套工作流状态。

## 数据与配置

一个 DataRoot 对应一个独立的本地 Dev Bridge 环境：

```text
<DataRoot>/
├── .devbridge-data-root.json
├── state/          # SQLite 持久化状态（devbridge.db）
├── worktrees/      # Bridge 管理的执行工作区
├── evidence/       # 工程证据
├── logs/           # Core 等进程的结构化日志
├── runtime/        # 运行时材料
└── artifacts/      # 运行产物
```

不传 `-data-root` 时，Core 使用系统用户配置目录下的 `DevBridge/data`；macOS 对应 `~/Library/Application Support/DevBridge/data`。Desktop 偏好设置独立保存，详细位置见 [Desktop 文档](desktop/README.md#dev-bridge-data-directory)。

- 新目录或空目录会初始化为新环境；已识别的 DataRoot 会打开原环境。
- 项目的原始 Git 仓库保留在所登记的位置，执行 worktree 位于 DataRoot 中。
- 同一 DataRoot 同时只允许一个 Core 写入；遇到占用冲突时先关闭占用它的 Core。
- 切换 DataRoot 不会迁移或合并数据。备份时先停止对应 Core，并保留需要的仓库和工作区。

Core 的模型配置可使用 `-allowed-models` 或 `-coding-agent-model-configs`，由同一 Core 向两个 adapter 发布。完整参数可通过各程序的 `-help` 查看；模型与推理配置见 [Local MCP 指南](docs/user-guide/local-mcp.md#running-it-locally)。

## 开发与测试

### Core 与 Web/MCP 界面

在仓库根目录执行：

```sh
go test ./...
go vet ./...
node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js
```

需要检查并发行为时运行 `go test -race ./...`。

### Desktop

Desktop 是独立的嵌套 Go 模块，需要单独检查：

```sh
cd desktop
go test ./...
go vet ./...
cd frontend
npm ci
npm test
npm run check
```

启动 Desktop 开发模式：

```sh
cd desktop
./scripts/build-sidecars.sh
cd frontend
npm ci
cd ..
wails dev
```

以上两组命令分别从仓库根目录开始。

### 工作流验收

仓库提供通过公开 HTTP/MCP 接口的本地工作流验收脚本。它会构建 Core 与两个 adapter，创建临时 Git 仓库，并检查从任务到 Git 交付及进程重启后的状态。

先把下面路径替换为已安装的**原生 Codex 可执行文件**路径：

```sh
DEVBRIDGE_PLATFORM_NATIVE_BINARY=/absolute/path/to/native/codex \
  ./scripts/verify-usable-project.sh
```

该脚本使用本地 Responses provider，保留证据到打印的目录；它验证本地协议和工作流，不代表真实在线模型质量或服务可用性。详细范围见[工作流验收说明](docs/testing/real-e2e-gates.md)，运行前置条件以当前脚本为准。

## 仓库结构

```text
cmd/
├── devbridge/             # Core 与诊断命令
├── devbridge-mcp/         # GPT adapter 进程入口
└── devbridge-local-mcp/   # Local MCP adapter 进程入口
internal/                 # 领域模型、工作流、运行时、Git、SQLite 与 HTTP/MCP
api/codex-schema/         # Codex 协议 schema
desktop/                 # 独立 Wails 模块和前端
docs/                    # 用户指南、架构、接口与验收记录
scripts/                 # 构建辅助、诊断与验收脚本
test/                    # 端到端测试 harness
```

`skeleton/` 是可选的本地流程约束目录，已被 Git 忽略，不随仓库克隆提供。公开测试使用仓库内的 Task 阶段词汇快照；存在本地 Skeleton 时会额外核对其约束。

## 兼容性与限制

- 当前为 Alpha 版本，尚未承诺稳定接口或跨版本数据兼容性。具体工作流以[用户指南](docs/user-guide/README.md)和[接口参考](docs/interfaces.md)为准；历史设计不代表当前支持的接口。
- Core 与 adapter 默认使用本机 loopback HTTP。它们的运行方式面向本机服务，部署时应保留这一边界。
- Codex 可执行文件、模型目录和账户可用能力需要匹配；可选的 App Server 跨 Core 会话连续性具有版本及协议约束，不保证所有中断都能恢复原会话。参见[连续性说明](docs/user-guide/getting-started.md#可选的-codex-会话连续性)。
- worktree 用于隔离工程工作区；它不等同于针对恶意 Coding Agent 的操作系统安全沙箱。
- Desktop 的签名、公证、自动更新和正式发布分发尚未包含在当前打包流程中。

## 文档导航

| 文档 | 内容 |
| --- | --- |
| [用户指南](docs/user-guide/README.md) | 产品模型、入门和推荐阅读顺序 |
| [开发工作流](docs/user-guide/workflow.md) | Review、批准、执行、验收与交付 |
| [FeatureArea 与 Review](docs/user-guide/feature-areas-and-reviews.md) | 持续工作与审查规则 |
| [决策与授权](docs/user-guide/decisions-and-authorization.md) | 用户确认与运行时权限 |
| [Desktop / Web Console](docs/user-guide/desktop-and-console.md) | 界面、运行会话和服务生命周期 |
| [Local MCP](docs/user-guide/local-mcp.md) | 本地客户端配置与调用合同 |
| [接口参考](docs/interfaces.md) | 工具、字段、状态与错误约定 |
| [Task / Execution 架构](docs/architecture/task-execution-v0.1.md) | 当前任务与执行模型 |
| [历史验收索引](docs/validation/README.md) | 按主题查阅报告、截图及原始证据归档 |
| [开发里程碑归档](docs/development-history.md) | 原 README 的实现历史 |

## 参与贡献

欢迎通过 [GitHub Issues](https://github.com/lazyun/devbridge/issues)报告问题或提出建议，也可以提交 Pull Request。问题报告请附上复现步骤、版本、操作系统和脱敏日志；代码改动请说明影响范围，并运行相关模块的检查。

## 许可证

本项目采用 [MIT License](LICENSE)，允许使用、修改、分发和商用，使用时需保留版权声明及许可证文本。

### Codex 运行兼容性

独立 App Server 已准入 `0.149.0`、`0.159.2` 和 `0.163.0`。现代版本使用分页读取回合历史；`0.149.0` 保留其既有读取路径。版本准入不代替后端、模型与工作区的实际资格检查。

在 Desktop / Web Console 的“运行会话”中，可以向仍在运行且没有待决授权的当前回合追加说明。发送回执区分已接收、拒绝和结果未知；结果未知时不会自动重发。追加说明不能替代任务修订、实施审批或最终验收。详见[运行中补充说明](docs/user-guide/workflow.md#supplementing-a-running-turn)。
