# Gofer

A small Go coding agent. Not Pi, not Claude Code — just enough to read, edit, and run.

Gofer 是一个用 Go 实现的命令行 Coding Agent。给定一个项目目录和一个自然语言指令，它会读取文件、编辑代码、执行 Shell 命令来完成任务。它的默认工具是 `read`、`write`、`edit`、`bash`，设计上参考了 [Pi](https://github.com/earendil-works/pi) 的运行时模型。

Gofer 本身是一个完整的 Agent，但更重要的身份是 [Agent 手记](https://arixbit.me) 系列的学习产物——13 篇文章从"什么是 LLM"一路走到"用 Go 实现一个最小 Coding Agent"。如果你对 Agent 的内部原理感兴趣，系列文章比这份源码更适合作为入口。

## 安装

```bash
git clone https://github.com/arixbit/gofer.git
cd gofer
go build -o ~/bin/gofer .
```

## 配置

创建用户配置文件（只需要 API Key）：

```bash
mkdir -p ~/.coding-agent
cp config.example.json ~/.coding-agent/config.json
chmod 600 ~/.coding-agent/config.json
```

编辑 `~/.coding-agent/config.json`，填入你的 API Key：

```json
{
  "provider": {
    "api_key": "sk-xxxxxxxxxxxxxxxx"
  }
}
```

默认使用 DeepSeek（模型 `deepseek-v4-flash`）。如果你的 API 不是 DeepSeek，加上 `base_url` 和 `model`：

```json
{
  "provider": {
    "api_key": "sk-xxxxxxxxxxxxxxxx",
    "base_url": "https://api.example.com/v1",
    "model": "your-model-name"
  }
}
```

只要填了 `base_url`，Gofer 就会自动走 OpenAI-compatible 协议。不需要手动指定 `type` 字段。

## 使用

进入你的项目目录，然后启动 Gofer：

```bash
cd ~/src/my-project
gofer
```

当前目录就是 Gofer 的工作区（workspace）。模型使用时只需要提供相对路径——`read` 和 `edit` 工具会拒绝工作区外的路径。

启动后进入 REPL 模式：

```
> 读一下 main.go，说说这个文件做了什么
> 给 Add 函数写一个单元测试
> 把第 12 行的 fmt.Println 改成 log.Println
```

DeepSeek 默认开启 Thinking Mode：模型在给出回答前会先进行推理，推理过程以 `💭 ` 前缀实时打印在终端，正文紧随其后。推理内容（`reasoning_content`）会被存入会话历史并在下一轮请求中原样回传，满足 DeepSeek 多轮工具调用对 reasoning 回传的协议约束。

内置命令：

| 命令 | 作用 |
|------|------|
| `/help` | 查看帮助 |
| `/history` | 查看当前会话历史 |
| `/resume <id>` | 恢复之前的会话 |
| `/new` | 新建会话 |

## 安全

Gofer 以启动时的用户权限运行。`bash` 不是沙箱——它可以直接执行你的 Shell 能执行的任何命令。

Gofer 做了基础的路径保护：`read`、`write`、`edit` 只接受工作区内的相对路径，拒绝绝对路径、`..` 和越界符号链接。但进程隔离仍然依赖运行环境（容器、虚拟机、受限账号）。

**不要在不信任的代码仓库或不受信任的模型提示下裸跑 Gofer。**

## 与 Pi 的关系

Gofer 的设计参考了 [Pi](https://github.com/earendil-works/pi) 的运行时架构：模型提出工具调用 → Runtime 执行 → 结果回填 → 模型继续。但 Gofer 不是一个 Pi 的移植版。它更小、更简单，没有会话树、流式事件、扩展系统和 TUI。

## 可选功能

### Skill

Skill 是按需加载的工作方法说明。启动时只暴露 Skill 的名称和简介，模型选择后才读取正文。

在 `~/.coding-agent/config.json` 中配置 Skill 目录：

```json
{
  "skills": ["/Users/me/.coding-agent/skills"]
}
```

每个 Skill 目录下需要有一个 `SKILL.md` 文件。

### MCP

通过 MCP 接入远程工具。配置 Streamable HTTP MCP Server 地址：

```json
{
  "mcp_servers": [
    {"name": "weather", "url": "http://127.0.0.1:3000/mcp"}
  ]
}
```

远端工具会自动加上 Server 名称前缀（如 `weather__forecast`），防止和本地工具重名。

## 开发

```bash
# 跑测试
go test ./...

# 跑 race detector
go test -race ./agent

# 静态检查
go vet ./...
```

## License

MIT
