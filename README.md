# QEMU 外设建模工具链

一个面向 QEMU 与嵌入式系统的外设建模工具链：从硬件参考资料中提取寄存器语义，
生成可审查的 Reg-IR 与 C 代码，并通过审批、构建和测试逐步落地到 QEMU 源码树。

项目当前以**外设建模**为核心，交互式命令行只是入口；生成、审查、应用和验证彼此隔离，
便于追踪每一步的输入、产物与证据。

当前版本：**preview 2.0**（外设建模流水线已落地，默认关闭）。

## 能力概览

```text
参考资料 → plan → extract → infer → emit → 人工审批 apply → verify
              │        │        │        │                  │
              │        │        │        └─ Reg-IR → C 产物  └─ ninja/qtest 证据
              │        │        └─ 补充访问属性、依赖与中断语义
              │        └─ 形成结构化寄存器中间表示
              └─ 明确外设范围、资料和验收目标
```

核心设计：以硬件资料为事实源，以 Reg-IR 为中间表示；生成结果先进入产物库，
只有经过人工确认才允许修改 QEMU 源码树。

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

# 运行 Modeling 流水线

这是本项目的核心能力，**默认关闭**。

## 为什么默认关

它比其它能力多一个危险维度：要写 **QEMU 源码树**，那不是 agent 私有的沙箱。
所以启用需要你显式给出落地目标，并且落地这一步永远需要人工逐个批准。

## 1. 启用

```bash
export OPENROUTER_API_KEY=sk-or-v1-...
export QEMU_AGENT_MODELING_ENABLED=true
export QEMU_AGENT_MODELING_QEMU_ROOT=/path/to/qemu          # 你的 QEMU 源码树
export QEMU_AGENT_MODELING_BUILD_DIR=/path/to/qemu/build    # verify 用，需已 configure 过
export QEMU_AGENT_WORKSPACE=/path/to/datasheets             # datasheet 放这儿，agent 才读得到
go run ./cmd/qemu-agent
```

两个路径都可以留空，对应不同的能力档位：

| 配置 | 能做什么 |
|---|---|
| 全不给 | `/modeling` 回答 `modeling is disabled` |
| 只给 `ENABLED` | 能规划、抽 Reg-IR、生成代码、出 diff；`apply` 回答 `modeling apply is not available in this configuration` |
| 加 `QEMU_ROOT` | 能 `apply` 落地 |
| 再加 `BUILD_DIR` | 能 `verify` 跑 ninja + qtest |

只给 `ENABLED` 是个真实可用的档位：在一台没有 QEMU checkout 的机器上读数据手册、产出
Reg-IR 和 C 代码，最后把 diff 拷走。流水线读数据手册用的是 **workspace** 根，`apply`
写文件用的是 **QEMU 树**根——两套工具根，所以少配一个不会让另一个失效。

`BUILD_DIR` 必须是**你自己已经 configure 好**的构建目录。配置 QEMU 需要只有你知道的参数
（target 列表、交叉编译器），流水线不会替你跑 `configure`，否则会构建出没人要的东西：

```bash
cd /path/to/qemu && mkdir -p build && cd build
../configure --target-list=riscv64-softmmu --enable-debug
```

## 2. 五个阶段

```text
plan     读需求与 skill，产出建模计划
extract  从 datasheet / 头文件抽出 Reg-IR（寄存器映射的结构化表示）
infer    补齐行为语义：复位值、副作用、中断
emit     生成 C 代码到暂存区，产出 diff 与 apply manifest —— 此时 QEMU 树零变化
verify   在 build 目录跑 ninja 与 qtest，产出 Evidence
```

## 3. 完整走一遍

```text
/modeling new k230 rmu
  → created modeling project mp-a1b2c3d4e5f6a7b8 at stage plan (pending)

/modeling advance mp-a1b2... --source=/path/to/datasheets/k230-rmu.txt 把 RMU 建成 sysbus 设备
/modeling advance mp-a1b2...          # extract：抽 Reg-IR
/modeling advance mp-a1b2...          # infer：补行为语义
/modeling advance mp-a1b2...          # emit：生成代码；QEMU 树此刻仍零变化

/modeling show mp-a1b2...             # 看状态与产物清单
/modeling diff mp-a1b2...             # 读 diff ← 人工审阅这一步是全部安全性的基础
/modeling apply mp-a1b2...            # 落地，逐个文件弹审批
/modeling advance mp-a1b2...          # verify：ninja + qtest
/modeling evidence mp-a1b2...         # 看构建与测试证据
```

要点：

- `--source=` 只在 extract 需要，指向 datasheet 的**文本**文件（PDF 请先转成文本），
  且必须在 workspace 之内。
- `emit` 跑完项目停在 `blocked / awaiting_apply`——这不是错误，是在等你审 diff。
  此时再 `advance` 会重跑 emit，而不是往下走；`verify` 只有 `apply` 之后才可达。
- 中途出错不用重开项目：

  ```text
  /modeling reset mp-a1b2... extract --confirm=mp-a1b2...
  ```

  回到某个阶段重跑，该阶段之后的产物会被清掉。确认词就是项目 id 本身。
- 进程被杀掉再启动，`/modeling show` 会告诉你上次死在哪个阶段。

## 4. 三条安全规则

1. **emit 不落地。** 生成的代码进内容寻址的产物库，不进 QEMU 源码树。要落地必须显式
   `/modeling apply`，它需要交互式渠道（有人能看 diff 并逐个批准），每次写入都经过
   `security.Executor`，和任何一次普通 `write` 受同样的策略与审计约束。
2. **建模产物永不自动进系统提示词。** 阶段结论是模型自己写的内容，回灌等于让它把自己的
   猜测当成事实。
3. **错误只报类别，不回显内容。** datasheet 片段、模型原文、工具 stdout 一律不进日志、
   不进项目记录、不进渠道回复。

项目状态与产物以 `0700` / `0600` 落盘：项目标题和产物名描述的是未发布硬件。

---

# 全部命令

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

# 当前能力

| 能力 | 说明 | 默认 |
|---|---|---|
| Agent loop | OpenAI 兼容协议（默认 OpenRouter），tool calling | 开 |
| 会话管理 | 多会话注册表、持久化、`/resume`、自动压缩上下文 | 开 |
| 安全工具执行 | 所有副作用经 `security.Executor`：策略判定 → 人工审批 → JSONL 审计 | 开 |
| 内置工具 | `read` / `write` / `bash`，均限制在 workspace 内 | 开 |
| 模型注册表 | 多 provider / 多模型，运行期 `/model` 切换 | 开 |
| 请求级事件流 | `run_started` / `turn_started` / `tool_*` / `stage_*` / `run_completed`，CLI 与 Telegram 各自渲染 | 开 |
| Telegram 渠道 | 白名单用户、并发限制、消息分片 | 关 |
| Skills | 操作员放置的 SKILL.md，索引进系统提示词，模型用 `use_skill` 取正文 | 开 |
| Memory | `/remember` 手工记忆 + 可选自动抽取候选、人工审批后入库 | 开 |
| **Modeling 流水线** | **五阶段外设建模，产物内容寻址、落地需人工审批** | **关** |

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

## Skills / Memory / Telegram

`QEMU_AGENT_SKILLS_ENABLED`、`QEMU_AGENT_SKILLS_DIR`、`QEMU_AGENT_MEMORY_ENABLED`、
`QEMU_AGENT_MEMORY_AUTO_EXTRACT`、`QEMU_AGENT_TELEGRAM_ENABLED`、`QEMU_AGENT_TELEGRAM_TOKEN`、
`QEMU_AGENT_TELEGRAM_ALLOWED_USER_IDS` 等；完整清单见 `internal/config/`。

# 排错

| 现象 | 原因与解法 |
|---|---|
| `OPENROUTER_API_KEY is required` | 没设 key；或想用本地模型则设 `QEMU_AGENT_PROVIDER=ollama` |
| `QEMU_AGENT_STREAM=true is not supported yet` | 流式尚未支持，去掉该变量 |
| `modeling is disabled` | 设 `QEMU_AGENT_MODELING_ENABLED=true` 并重启 |
| `apply is unavailable: this build has no QEMU source tree configured` | 没设 `QEMU_AGENT_MODELING_QEMU_ROOT` |
| `modeling apply is not available in this configuration` | 同上，来自更底层：applier 本身是禁用实现 |
| `apply needs an interactive channel` | 在交互式终端里跑，不要用管道或 `-p`（这条会先于上面两条触发） |
| `extract needs at least one --source=<path> to read` | extract 阶段要 `--source=` 指向 datasheet 文本 |
| extract 说读不到 `--source` 的文件 | datasheet 要放在 `QEMU_AGENT_WORKSPACE` 里，不是 QEMU 树里 |
| 工具报路径不可达 | 目标不在 `QEMU_AGENT_WORKSPACE` 内；把 workspace 指对 |
| `apply_rejected` | 常见于项目还没到 emit 阶段就 apply，或目标文件已存在 |

# 项目结构

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

# 开发

```bash
gofmt -l cmd internal     # 无输出
go vet ./...              # 无输出
go test ./...             # 全绿
go build ./...            # 成功
```

跨层集成测试见 `internal/app/modeling_integration_test.go`：用 stub 模型与
recording executor 跑 `new → advance×4 → diff → apply → verify`，断言 apply 之前
QEMU 树零变化、每阶段留下产物、进程重启后状态一致、executor 收到的调用集合没有旁路。
