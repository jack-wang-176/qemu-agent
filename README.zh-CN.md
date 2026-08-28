# QEMU 外设建模工具链

一个面向 QEMU 与嵌入式系统的外设建模工具链：从硬件参考资料中提取寄存器语义，
生成可审查的 Reg-IR 与 C 代码，并通过审批、构建和测试逐步落地到 QEMU 源码树。

项目当前以**外设建模**为唯一主线路，交互式命令行只是入口；生成、审查、应用和验证彼此隔离，
便于追踪每一步的输入、产物与证据。

当前版本：**专业建模 Agent**（主线路已落地，必须显式启用）。

## 能力概览

```text
参考资料 → plan → extract → infer → emit → [v1: awaiting_apply]
              │        │        │        │
              │        │        │        └─ Reg-IR → C 产物与可审查 diff
              │        │        └─ 补充访问属性、依赖与中断语义
              │        └─ 形成结构化寄存器中间表示
              └─ 明确外设范围、资料和验收目标

后续版本：awaiting_apply → 人工审批 apply → verify → ninja/qtest Evidence
```

核心设计：以硬件资料为事实源，以 Reg-IR 为中间表示；生成结果只进入产物库。
v1 不提供修改 QEMU 源码树的生产入口。

---

# 快速开始

## 0. 前置条件

- **Go 1.26+**（`go.mod` 声明 `go 1.26.0`）
- 一个模型来源，任选其一：
  - OpenRouter API key（默认，最省事）
  - OpenAI API key
  - 本地 Ollama（**不需要 key**）

无需 `make`，项目没有 Makefile，全部用标准 go 命令。

## 1. 三十秒跑起来

```bash
cd qemu-agent
export OPENROUTER_API_KEY=sk-or-v1-...     # 必填，否则启动即报 "OPENROUTER_API_KEY is required"
export QEMU_AGENT_MODELING_ENABLED=true    # v1 专业建模主线路必填
go run ./cmd/qemu-agent
```

看到 `> ` 提示符就成功了。输入 `/help` 看全部命令，`/exit` 或 Ctrl-D 退出。

想只问一句就退出（脚本友好）：

```bash
go run ./cmd/qemu-agent -p "列出 hw/misc 下的设备文件"
```

编译成二进制再用（推荐日常使用，启动快）：

```bash
go build -o bin/qemu-agent ./cmd/qemu-agent
./bin/qemu-agent
```

## 2. 用本地 Ollama（不花钱、不联网）

```bash
ollama serve                                # 另开一个终端
ollama pull qwen2.5-coder:7b

export QEMU_AGENT_PROVIDER=ollama
export QEMU_AGENT_MODEL=qwen2.5-coder:7b
export QEMU_AGENT_MODELING_ENABLED=true
go run ./cmd/qemu-agent
```

Ollama 走本地 `http://localhost:11434/v1`，不需要任何 API key。

## 3. 命令行 flag

| flag | 作用 |
|---|---|
| `-p <prompt>` | 单次执行模式：跑完这一句就退出，不进 REPL |
| `-model <name>` | 覆盖本次运行的模型 |
| `-provider <name>` | 覆盖 provider：`openrouter` / `openai` / `ollama` |
| `-max-turns <n>` | 覆盖单次请求最大轮数 |

## 4. 数据落在哪

| 内容 | 位置 |
|---|---|
| 会话 / skill / memory / modeling | `<用户配置目录>/qemu-agent/`，用 `QEMU_AGENT_DATA_DIR` 覆盖 |
| 工具审计日志 | `<DataDir>/audit/tools.jsonl`，每次工具调用一行 |
| 工具可读写范围 | **仅** `QEMU_AGENT_WORKSPACE`（默认当前目录） |

用户配置目录在 macOS 是 `~/Library/Application Support`，Linux 是 `~/.config`。

第一次运行会自己建目录，不用手工准备。想换个干净环境试，指一个临时目录就行：

```bash
QEMU_AGENT_DATA_DIR=/tmp/qa-data QEMU_AGENT_WORKSPACE=/tmp/qa-ws go run ./cmd/qemu-agent
```

工具被限制在 workspace 内是硬约束：`read` / `write` / `bash` 都拿不到 workspace 之外的路径。
所以**如果你想让 agent 读你的 QEMU 代码，就把 workspace 指到 QEMU 树**：

```bash
QEMU_AGENT_WORKSPACE=/path/to/qemu go run ./cmd/qemu-agent
```

## 5. 危险操作会停下来问你

默认 `ask-dangerous` 模式：`write` 和 `bash` 这类有副作用的工具在执行前会在终端弹出审批，
你回车确认或拒绝。所以**建议在交互式终端里用**——非交互渠道无人可问，一律 fail-closed 拒绝。

```bash
export QEMU_AGENT_TOOL_APPROVAL_TIMEOUT=5m    # 默认 5 分钟，超时视为拒绝
```

---

# 运行专业建模工作流

v1 不再把建模作为偶尔调用的命令或工具。CLI 与 Telegram 收到的普通文本统一进入：

```text
channel -> Application -> modelingagent.Runner -> modelingworkflow.Controller
        -> modelingapi.Service -> pipelineapi.Engine -> five-stage implementation
```

启动必须显式设置：

```bash
export OPENROUTER_API_KEY=sk-or-v1-...
export QEMU_AGENT_MODELING_ENABLED=true
export QEMU_AGENT_MODELING_AUTO_APPLY=false
export QEMU_AGENT_WORKSPACE=/path/to/modeling-workspace
go run ./cmd/qemu-agent
```

然后直接用自然语言工作，例如：

```text
请创建一个 K230 RMU 的 QEMU 外设建模项目，目标是 sysbus 设备。
资料位于 datasheets/k230-rmu.txt。
继续当前项目。
展示当前状态。
读取生成的 diff。
```

解释层会把对话约束为 `start`、`continue`、`inspect`、`provide_input`、
`read_artifact`、`evidence` 或 `start_new` 意图；用户不能通过对话绕过项目状态机。
同一个 channel 会话绑定一个当前项目，绑定、会话、项目和产物分别持久化。

五阶段实现仍为 `plan -> extract -> infer -> emit -> verify`，但 v1 生产主线路只自动推进到
`emit` 完成后的 `awaiting_apply` 边界。此时 QEMU 树没有变化，回复只展示可审查的 diff
产物；生产组合根不构造 Apply executor，也不提供 Apply 命令，因此 `verify` 在 v1 主线路
不可达。Apply 与后续 Verify 将在具备独立人工审核协议的版本中接入。

必须遵守三条安全边界：

1. `emit` 只写内容寻址产物库，不写 QEMU 源码树。
2. 建模产物不会自动回灌系统提示词；需要读取时按显式 Artifact 引用取回。
3. datasheet 内容、模型原文和工具输出不会作为内部错误细节直接返回或写入普通日志。

`QEMU_AGENT_MODELING_ENABLED` 默认仍为 `false`，用于要求部署者显式接受专业建模产品配置；
配置为 `false` 时会拒绝启动，而不会退回旧的通用 Agent。

---

# 全部命令

```text
/help                                          列出命令
/new /reset /sessions /resume <id> /history    会话管理
/compact                                       手工压缩上下文
/skills [list|show <name>]                     查看 skill
/remember [--kind=<kind>] [--scope=<scope>] <text>
/memory list|search <text>|show <id>|forget <id>|pending|approve <id>|reject <id>
/exit
```

`/model` 与 `/modeling` 已移除。建模模型在启动时固定解析，建模操作通过普通文本进入强约束
workflow；未知旧命令返回可恢复的 `unknown command`，不会触发隐藏的第二控制面。

# 当前能力

| 能力 | 说明 | 默认 |
|---|---|---|
| 专业建模主循环 | 普通文本解释为受限意图并进入固定建模 workflow | 需显式启用 |
| 会话管理 | 多会话注册表、持久化、`/resume`、自动压缩上下文 | 开 |
| 安全工具执行 | 所有副作用经 `security.Executor`：策略判定 → 人工审批 → JSONL 审计 | 开 |
| 内置工具 | `read` / `write` / `bash`，均限制在 workspace 内 | 开 |
| 模型注册表 | 多 provider / 多模型；建模模型在启动时固定解析 | 开 |
| 请求级事件流 | `run_started` / `turn_started` / `tool_*` / `stage_*` / `run_completed`，CLI 与 Telegram 各自渲染 | 开 |
| Telegram 渠道 | 白名单用户、并发限制、消息分片 | 关 |
| Skills | 操作员放置的 SKILL.md，索引进系统提示词，模型用 `use_skill` 取正文 | 开 |
| Memory | `/remember` 手工记忆 + 可选自动抽取候选、人工审批后入库 | 开 |
| **Modeling 流水线** | **v1 推进至 `awaiting_apply`，产物内容寻址，QEMU 树零写入** | **必需** |

流式输出（`QEMU_AGENT_STREAM`）尚未支持，置 `true` 会启动失败。

# 配置速查

全部配置只经环境变量，只由 `internal/config` 读取。

## 模型与 provider

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENROUTER_API_KEY` | — | provider 为 openrouter 时必填 |
| `OPENAI_API_KEY` | — | provider 为 openai 时必填 |
| `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1` | |
| `QEMU_AGENT_PROVIDER` | `openrouter` | `openrouter` / `openai` / `ollama` |
| `QEMU_AGENT_MODEL` | `openai/gpt-4o-mini` | |
| `QEMU_AGENT_MODELS_JSON` | — | 自定义模型目录（JSON） |
| `QEMU_AGENT_MAX_TURNS` | `15` | 单次请求最大轮数 |

## 路径与上下文

| 变量 | 默认 | 说明 |
|---|---|---|
| `QEMU_AGENT_DATA_DIR` | `<用户配置目录>/qemu-agent` | 会话、skill、memory、modeling 的根 |
| `QEMU_AGENT_WORKSPACE` | 当前目录 | 工具唯一可触碰的树 |
| `QEMU_AGENT_MAX_CONTEXT_TOKENS` | `160000` | 超过则自动压缩 |
| `QEMU_AGENT_KEEP_RECENT_TURNS` | `4` | 压缩时保留的完整轮数 |

## 安全与审计

| 变量 | 默认 | 说明 |
|---|---|---|
| `QEMU_AGENT_TOOL_SECURITY_MODE` | `ask-dangerous` | 危险工具需审批 |
| `QEMU_AGENT_TOOL_APPROVAL_TIMEOUT` | `5m` | 超时视为拒绝 |
| `QEMU_AGENT_TOOL_AUDIT_PATH` | `<DataDir>/audit/tools.jsonl` | 每次工具调用一行 |
| `QEMU_AGENT_TOOL_TIMEOUT` | — | 单个工具墙钟上限 |

审计日志能完整还原一次建模读了哪些文件、跑了哪些命令、写了哪些文件；参数与输出按上限截断并脱敏。

## Modeling

| 变量 | 默认 | 说明 |
|---|---|---|
| `QEMU_AGENT_MODELING_ENABLED` | `false` | v1 启动必须显式设为 `true` |
| `QEMU_AGENT_MODELING_QEMU_ROOT` | 空 | v1 主线路不使用，预留给后续受审 Apply 接入 |
| `QEMU_AGENT_MODELING_BUILD_DIR` | 空 | v1 主线路不使用，Verify 在 Apply 后才可达 |
| `QEMU_AGENT_MODELING_DIR` | `<DataDir>/modeling` | 项目状态与产物 |
| `QEMU_AGENT_MODELING_MODEL` | 空 | 阶段用的模型；空 = 复用会话默认 |
| `QEMU_AGENT_MODELING_STAGE_TIMEOUT` | `10m` | 单阶段墙钟上限 |
| `QEMU_AGENT_MODELING_MAX_PROJECTS` | `1000` | 单 workspace 项目数上限 |
| `QEMU_AGENT_MODELING_MAX_ARTIFACT_BYTES` | `1 MiB` | 单产物上限 |
| `QEMU_AGENT_MODELING_MAX_PROJECT_BYTES` | `8 MiB` | 单项目产物总和上限 |
| `QEMU_AGENT_MODELING_AUTO_APPLY` | `false` | v1 必须保持 `false`，否则拒绝启动 |

## Skills / Memory / Telegram

`QEMU_AGENT_SKILLS_ENABLED`、`QEMU_AGENT_SKILLS_DIR`、`QEMU_AGENT_MEMORY_ENABLED`、
`QEMU_AGENT_MEMORY_AUTO_EXTRACT`、`QEMU_AGENT_TELEGRAM_ENABLED`、`QEMU_AGENT_TELEGRAM_TOKEN`、
`QEMU_AGENT_TELEGRAM_ALLOWED_USER_IDS` 等；完整清单见 `internal/config/`。

# 排错

| 现象 | 原因与解法 |
|---|---|
| `OPENROUTER_API_KEY is required` | 没设 key；或想用本地模型则设 `QEMU_AGENT_PROVIDER=ollama` |
| `QEMU_AGENT_STREAM=true is not supported yet` | 流式尚未支持，去掉该变量 |
| `professional modeling requires QEMU_AGENT_MODELING_ENABLED=true` | 显式设置该变量并重启；不会降级到旧 Agent |
| `professional modeling requires QEMU_AGENT_MODELING_AUTO_APPLY=false` | 删除该变量或设为 `false` |
| workflow 询问 source | 用自然语言提供 workspace 内的资料相对路径 |
| extract 说读不到 source | datasheet 必须位于 `QEMU_AGENT_WORKSPACE` 内 |
| 工具报路径不可达 | 目标不在 `QEMU_AGENT_WORKSPACE` 内；把 workspace 指对 |
| `apply_rejected` | 常见于项目还没到 emit 阶段就 apply，或目标文件已存在 |

# 项目结构

```text
cmd/qemu-agent/          入口：flag 解析 → config → app.Build → Runtime.Run
internal/
  app/                   组合根。Build 是唯一选择具体实现的地方
    build/               Pipeline、Service、Workflow 与专业 Runner 的分层装配
    commands*.go         斜杠命令族
  modelingagent/         会话投影、意图解释、事件桥与专业 workflow Runner
  modelingworkflow/      对话意图、会话项目绑定与强约束工作流控制器
  agent/                 共享 RunInput 等基础类型；生产组合根不构造通用 Agent
  session/               会话注册表与持久化
  channel/               cli/ 与 telegram/ 两个渠道
  runstream/             请求级事件协议
  llm/                   provider 与模型注册表
  tools/                 Tool 接口、builtin 实现
    security/            策略、审批、审计、Executor —— 副作用唯一出口
  modeling/              五阶段流水线：状态机、产物库、各阶段、applier
  skills/ memory/ prompt/  知识层
  config/ contextmgr/ obs/
```

架构约束（不允许回退）：env 只由 `config` 读取；`app.Build` 是唯一实现选择点；
`agent` 不知道 CLI/Telegram 的存在；工具没有 `Executor` 旁路；建模每阶段都有产物或证据。

# 开发

```bash
gofmt -l cmd internal     # 无输出
go vet ./...              # 无输出
go test ./...             # 全绿
go build ./...            # 成功
```

跨层 Pipeline 集成测试见 `internal/app/modeling_integration_test.go`；生产接线 Gate
位于 `internal/app/build_test.go`，并断言 `Application` 实际持有 `modelingagent.Runner`。
