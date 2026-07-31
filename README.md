# qemu-agent

一个面向 QEMU / 嵌入式外设建模的手写 Claude 风格智能体。

代码起源于 codecrafters 上 "Build Your own Claude Code" 挑战的 Go 实现，
现已剥离平台绑定，围绕 **QEMU 外设半自动 / 全自动建模** 这一核心场景演进。

当前版本：**preview 2.0**（I1–I8 全部落地）。

## 当前能力

| 能力 | 说明 | 默认 |
|---|---|---|
| Agent loop | OpenAI 兼容协议（默认 OpenRouter），tool calling | 开 |
| 会话管理 | 多会话注册表、持久化、`/resume`、自动压缩上下文 | 开 |
| 安全工具执行 | 所有副作用经 `security.Executor`：策略判定 → 人工审批 → JSONL 审计 | 开 |
| 内置工具 | `read` / `write` / `bash`，均限制在 workspace 内 | 开 |
| 模型注册表 | 多 provider / 多模型，运行期 `/model` 切换 | 开 |
| 请求级事件流 | `run_started` / `turn_started` / `tool_*` / `stage_*` / `run_completed`，CLI 与 Telegram 各自渲染（`text_delta` 需流式，暂未启用） | 开 |
| Telegram 渠道 | 白名单用户、并发限制、消息分片 | 关 |
| Skills | 操作员放置的 SKILL.md，索引进系统提示词，模型用 `use_skill` 取正文 | 开 |
| Memory | `/remember` 手工记忆 + 可选自动抽取候选、人工审批后入库 | 开 |
| **Modeling 流水线** | **五阶段外设建模：plan → extract → infer → emit → verify，产物内容寻址、落地需人工审批** | **关** |

### Modeling 流水线是什么

把一份 datasheet 变成一个可编译、可 qtest 的 QEMU 外设，拆成五个可中断、可重跑、每步都留证据的阶段：

```text
plan     读需求与 skill，产出建模计划
extract  从 datasheet / 头文件抽出 Reg-IR（寄存器映射的结构化表示）
infer    补齐行为语义：复位值、副作用、中断
emit     生成 C 代码到暂存区，产出 diff 与 apply manifest —— 此时 QEMU 树零变化
verify   在已配置好的 build 目录跑 ninja 与 qtest，产出 Evidence
```

三条贯穿始终的规则：

1. **emit 不落地。** 生成的代码进内容寻址的产物库，不进 QEMU 源码树。要落地必须显式 `/modeling apply`，它需要交互式渠道（有人能看 diff 并逐个批准写入），并且每次写入都经过 `security.Executor`。
2. **建模产物永不自动进系统提示词。** 阶段结论是模型自己写的内容，回灌等于让它把自己的猜测当成事实。
3. **错误只报类别，不回显内容。** datasheet 片段、模型原文、工具 stdout 一律不进日志、不进项目记录、不进渠道回复。

## 运行

### 1. 最小启动

```bash
export OPENROUTER_API_KEY=sk-...          # 必填
go run ./cmd/qemu-agent                   # 交互式 REPL
go run ./cmd/qemu-agent -p "读一下 hw/misc 下有哪些设备"   # 单次执行
```

可用 flag：`-p <prompt>`、`-model`、`-provider`、`-max-turns`。

数据默认落在用户配置目录下的 `qemu-agent/`（macOS 是
`~/Library/Application Support/qemu-agent`，Linux 是 `~/.config/qemu-agent`），
可用 `QEMU_AGENT_DATA_DIR` 覆盖；workspace 默认是当前目录（`QEMU_AGENT_WORKSPACE`）——
工具只能读写 workspace 之内的路径。

### 2. 启用 Modeling 流水线

Modeling 默认关闭，因为它比其它能力多一个危险维度：它要写 **QEMU 源码树**，
那不是 agent 私有的沙箱。启用需要显式给出落地目标：

```bash
export OPENROUTER_API_KEY=sk-...
export QEMU_AGENT_MODELING_ENABLED=true
export QEMU_AGENT_MODELING_QEMU_ROOT=/path/to/qemu        # 你的 QEMU 源码树
export QEMU_AGENT_MODELING_BUILD_DIR=/path/to/qemu/build  # verify 用，需已 configure 过
go run ./cmd/qemu-agent
```

`QEMU_AGENT_MODELING_BUILD_DIR` 必须是**你自己已经 configure 好**的构建目录：
配置 QEMU 需要只有操作员知道的参数（target 列表、交叉编译器），流水线不会替你跑
`configure`，否则会构建出没人要的东西。留空则 verify 阶段不可用，其余阶段照常工作。

同理，`QEMU_AGENT_MODELING_QEMU_ROOT` 留空是合法的：此时能规划、能抽 Reg-IR、能生成代码，
只是 `apply` 会明确回答"这个构建没有配置 QEMU 源码树"，而不是写到一半失败。

### 3. 走一遍完整流程

```text
/modeling new k230 rmu                                    # 建项目，返回 mp-xxxx
/modeling advance mp-xxxx --source=/docs/k230-rmu.pdf.txt 把 RMU 建成 sysbus 设备
/modeling advance mp-xxxx                                 # extract：抽 Reg-IR
/modeling advance mp-xxxx                                 # infer：补行为语义
/modeling advance mp-xxxx                                 # emit：生成代码，QEMU 树仍零变化
/modeling show mp-xxxx                                    # 看状态与产物清单
/modeling diff mp-xxxx                                    # 读 diff（人工审阅这一步是全部安全性的基础）
/modeling apply mp-xxxx                                   # 落地，逐个文件弹审批
/modeling advance mp-xxxx                                 # verify：ninja + qtest，产出 Evidence
/modeling evidence mp-xxxx                                # 看构建与测试证据
```

中途出错不用重开项目：`/modeling reset mp-xxxx <stage> --confirm=mp-xxxx` 回到某个阶段重跑，
该阶段之后的产物会被清掉。进程被杀掉再启动，`/modeling show` 会显示上次死在哪个阶段。

### 4. 全部命令

```text
/help                                          列出命令
/new /reset /sessions /resume <id> /history    会话管理
/compact                                       手工压缩上下文
/model [list|<alias|provider:model>]           查看 / 切换模型
/skills [list|show <name>]                     查看 skill
/remember [--kind=<kind>] [--scope=<scope>] <text>
/memory list|search <text>|show <id>|forget <id>|pending|approve <id>|reject <id>
/modeling new <title>|list|show <id>|advance <id> [--stage=<stage>] [--source=<path>] [request]
         |diff <id>|apply <id>|evidence <id>|reset <id> <stage> --confirm=<id>
/exit
```

## 配置

全部配置只经环境变量，只由 `internal/config` 读取。常用项：

### 模型与 provider

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENROUTER_API_KEY` / `OPENAI_API_KEY` | — | 至少一个必填 |
| `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1` | |
| `QEMU_AGENT_PROVIDER` | `openrouter` | `openrouter` / `openai` / `ollama` |
| `QEMU_AGENT_MODEL` | `openai/gpt-4o-mini` | |
| `QEMU_AGENT_MODELS_JSON` | — | 自定义模型目录（JSON） |
| `QEMU_AGENT_STREAM` | `false` | 尚未支持，置 `true` 会启动失败 |
| `QEMU_AGENT_MAX_TURNS` | `15` | 单次请求最大轮数 |

### 路径与上下文

| 变量 | 默认 | 说明 |
|---|---|---|
| `QEMU_AGENT_DATA_DIR` | `<用户配置目录>/qemu-agent` | 会话、skill、memory、modeling 的根 |
| `QEMU_AGENT_WORKSPACE` | 当前目录 | 工具唯一可触碰的树 |
| `QEMU_AGENT_MAX_CONTEXT_TOKENS` | `160000` | 超过则自动压缩 |
| `QEMU_AGENT_KEEP_RECENT_TURNS` | `4` | 压缩时保留的完整轮数 |

### 安全与审计

| 变量 | 默认 | 说明 |
|---|---|---|
| `QEMU_AGENT_TOOL_SECURITY_MODE` | `ask-dangerous` | 危险工具需审批 |
| `QEMU_AGENT_TOOL_APPROVAL_TIMEOUT` | `5m` | 超时视为拒绝 |
| `QEMU_AGENT_TOOL_AUDIT_PATH` | `<DataDir>/audit/tools.jsonl` | 每次工具调用一行 |
| `QEMU_AGENT_TOOL_TIMEOUT` | — | 单个工具墙钟上限 |

审计日志能完整还原一次建模读了哪些文件、跑了哪些命令、写了哪些文件；参数与输出按上限截断并脱敏。

### Modeling

| 变量 | 默认 | 说明 |
|---|---|---|
| `QEMU_AGENT_MODELING_ENABLED` | `false` | 总开关 |
| `QEMU_AGENT_MODELING_QEMU_ROOT` | 空 | QEMU 源码树；空 = `apply` 不可用 |
| `QEMU_AGENT_MODELING_BUILD_DIR` | 空 | 已 configure 的构建目录；空 = `verify` 不可用 |
| `QEMU_AGENT_MODELING_DIR` | `<DataDir>/modeling` | 项目状态与产物 |
| `QEMU_AGENT_MODELING_MODEL` | 空 | 阶段用的模型；空 = 复用会话默认 |
| `QEMU_AGENT_MODELING_STAGE_TIMEOUT` | `10m` | 单阶段墙钟上限 |
| `QEMU_AGENT_MODELING_MAX_PROJECTS` | `1000` | 单 workspace 项目数上限 |
| `QEMU_AGENT_MODELING_MAX_ARTIFACT_BYTES` | `1 MiB` | 单产物上限 |
| `QEMU_AGENT_MODELING_MAX_PROJECT_BYTES` | `8 MiB` | 单项目产物总和上限 |
| `QEMU_AGENT_MODELING_AUTO_APPLY` | `false` | 置 true 需同时给 QEMU_ROOT；**不建议** |

项目状态与产物以 `0700` / `0600` 落盘：项目标题和产物名描述的是未发布硬件。

### Skills / Memory / Telegram

`QEMU_AGENT_SKILLS_ENABLED`、`QEMU_AGENT_SKILLS_DIR`、`QEMU_AGENT_MEMORY_ENABLED`、
`QEMU_AGENT_MEMORY_AUTO_EXTRACT`、`QEMU_AGENT_TELEGRAM_ENABLED`、`QEMU_AGENT_TELEGRAM_TOKEN`、
`QEMU_AGENT_TELEGRAM_ALLOWED_USER_IDS` 等；完整清单见 `internal/config/`。

## 目录结构

```text
cmd/qemu-agent/          入口：flag 解析 → config → app.Build → Runtime.Run
internal/
  app/                   组合根。Build 是唯一选择具体实现的地方
    build/               各能力的装配：tool manager、knowledge、modeling
    commands*.go         斜杠命令族
  agent/                 Agent loop：模型调用、tool call 分派、事件发射
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

## 开发

```bash
gofmt -l cmd internal     # 无输出
go vet ./...              # 无输出
go test ./...             # 全绿
go build ./...            # 成功
```

跨层集成测试见 `internal/app/modeling_integration_test.go`：用 stub 模型与
recording executor 跑 `new → advance×4 → diff → apply → verify`，断言 apply 之前
QEMU 树零变化、每阶段留下产物、进程重启后状态一致、executor 收到的调用集合没有旁路。

## License

继承自上游 codecrafters starter 模板（MIT 友好），仅供学习与研究。
