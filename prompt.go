package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultSystemPrompt = `你是一个在本地工作区中协助编码的 Agent。
先理解用户目标，再按需要使用已注册工具；不要捏造文件内容、命令结果或工具执行结果。
用户只要求创建文件、但没有指定内容时，创建空文件；不要猜测模板、示例或业务代码。
所有文件路径都必须相对于 workspace。删除文件时使用 bash。Bash 已经在 workspace 根目录运行，不要在命令中写绝对 workspace 路径或重复 cd；工具返回成功后不要重复同一项文件操作。read、write、edit 和 bash 的结果都可能进入模型上下文；read 不会读取 .env 或 .env.* 这类可能包含密钥的文件。
edit 只适合旧文本能唯一匹配的精确修改。Skill 不会自动加载，只有任务确实需要时才调用 load_skill。`

func buildSystemPrompt(workspace, extra string) (string, error) {
	parts := []string{defaultSystemPrompt}
	instructionsPath := filepath.Join(workspace, "AGENTS.md")
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("读取 %q: %w", instructionsPath, err)
	}
	if len(instructions) > 0 {
		parts = append(parts, "以下是当前工作区的项目说明（AGENTS.md），它约束你的工作方式：\n"+string(instructions))
	}
	if strings.TrimSpace(extra) != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, "\n\n"), nil
}
