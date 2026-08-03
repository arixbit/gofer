package agent

import (
	"context"
)

// Memory 管理对话历史的 token 预算和压缩。
// 对话历史由调用方通过 Request.History 传入并保存。
type Memory interface {
	// ShouldCompress 判断是否需要压缩
	ShouldCompress(totalTokens int) bool

	// Compress 压缩记忆（截断或摘要）
	Compress(ctx context.Context, messages []Message) []Message
}

// InMemoryMemory 是 Memory 的最简单实现，使用字符数粗略估算并按轮次截断。
type InMemoryMemory struct {
	maxTokens int
}

// NewInMemoryMemory 创建内存记忆
func NewInMemoryMemory(maxTokens int) *InMemoryMemory {
	return &InMemoryMemory{
		maxTokens: maxTokens,
	}
}

func (m *InMemoryMemory) ShouldCompress(totalTokens int) bool {
	return totalTokens >= m.maxTokens
}

func (m *InMemoryMemory) Compress(ctx context.Context, messages []Message) []Message {
	// 简单策略：从头开始删最早的对话轮次，直到 token 数降到预算内
	for len(messages) >= 2 {
		total := 0
		for _, msg := range messages {
			for _, block := range msg.Content {
				// 粗略估算：每字符约 0.5 token。tool_use 的 JSON 参数也计入，
				// 否则 write 一次大文件内容会被估成 0 token。
				total += len(block.Text()) / 2
				if block.Type() == "tool_use" {
					total += len(block.Input()) / 2
				}
			}
		}
		if total <= m.maxTokens {
			break
		}

		// 按完整轮次边界删除（user 消息是分界点）：找到第二条 user 消息，删掉之前的所有内容。
		// 保证 assistant 的 tool_calls 和对应的 tool 结果不会因简单 messages[2:] 而分离。
		cutIdx := 1
		for cutIdx < len(messages) && !isUserTurnBoundary(messages[cutIdx]) {
			cutIdx++
		}
		if cutIdx >= len(messages) {
			break // 只剩最后一轮，不能再删
		}
		messages = messages[cutIdx:]
	}
	return messages
}

// isUserTurnBoundary 判断一条消息是否为真正的用户对话轮次分界点。
// 只有包含 text block 的 user 消息才被视为轮次边界。
// 纯 tool_result 的 user 消息（框架内部 tool result 用 Role: "user" 存储）
// 不能作为截断分界点——否则会留下孤立 tool result，provider 转换后缺少
// 对应的 assistant tool_calls，API 调用可能 400。
func isUserTurnBoundary(msg Message) bool {
	if msg.Role != "user" {
		return false
	}
	for _, block := range msg.Content {
		if block.Type() == "text" {
			return true
		}
	}
	return false
}
