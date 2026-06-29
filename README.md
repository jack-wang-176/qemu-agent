# qemu-agent

一个面向 QEMU / 嵌入式外设建模的手写 Claude 风格智能体。

代码起源于 codecrafters 上 "Build Your own Claude Code" 挑战的 Go 实现，
现已剥离 codecrafters 平台绑定，迁移至 `qemu/qemu-agent`，
后续将围绕 **QEMU 外设半自动 / 全自动建模** 这一核心场景持续演进。

## 当前能力

- 基于 OpenAI 兼容协议 (默认走 OpenRouter) 的 Agent loop
- 工具调用（tool calling）框架，支持注册任意 `Tool` 实现
- 内置工具：
  - `read`：读取本地文件
  - `write`：写入本地文件
  - `bash`：执行 shell 命令

## 运行

```bash
export OPENROUTER_API_KEY=...        # 必填
export OPENROUTER_BASE_URL=...       # 可选，默认 https://openrouter.ai/api/v1
go run ./app -p "your prompt"
```

## 目录结构

```
app/
  main.go            # Agent 主循环 + 模型调用
  message.go         # 消息容器（占位，后续会替换为完整 session）
  tool/
    tool.go          # Tool 接口
    manager.go       # Tool 注册 & 调度
    tools/
      bash.go        # 执行 shell
      read.go        # 读文件
      write.go       # 写文件
```

## 演进路线

详见 `qemu/claude-suggestion/` 下的四份建议文档：

1. `01-general-agent-gaps.md` —— 通用 Agent 缺失功能补充
2. `02-qemu-peripheral-modeling.md` —— 面向 QEMU 嵌入式外设建模的能力适配（**核心**）
3. `03-weknora-inspired-enhancements.md` —— 借鉴 WeKnora 的能力扩展
4. `04-roadmap-and-final-architecture.md` —— 总演进路线与最终形态

## License

继承自上游 codecrafters starter 模板（MIT 友好），仅供学习与研究。
