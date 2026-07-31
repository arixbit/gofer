package agent

import (
	"context"
	"strings"
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
				total += len(block.Text()) / 2 // 粗略估算：每字符约 0.5 token
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

// TruncationMemory 基于精确 token 计数的截断记忆
type TruncationMemory struct {
	maxTokens int
	provider  ModelProvider // 用于精确计数
}

// NewTruncationMemory 创建截断记忆
func NewTruncationMemory(provider ModelProvider, maxTokens int) *TruncationMemory {
	return &TruncationMemory{
		maxTokens: maxTokens,
		provider:  provider,
	}
}

func (m *TruncationMemory) ShouldCompress(totalTokens int) bool {
	return totalTokens >= m.maxTokens
}

func (m *TruncationMemory) Compress(ctx context.Context, messages []Message) []Message {
	// 按完整轮次边界删除（user 消息是分界点）
	for {
		count, err := m.provider.CountTokens(ctx, messages)
		if err != nil {
			return messages
		}
		if count <= m.maxTokens {
			return messages
		}
		if len(messages) == 0 {
			break
		}
		// 按完整轮次边界删除（user 消息是分界点）
		cutIdx := 1
		for cutIdx < len(messages) && !isUserTurnBoundary(messages[cutIdx]) {
			cutIdx++
		}
		if cutIdx >= len(messages) {
			break
		}
		messages = messages[cutIdx:]
	}
	return messages
}

// SummaryMemory 基于摘要的记忆压缩
type SummaryMemory struct {
	*TruncationMemory // 嵌入截断能力，摘要失败时回退
	summaryProvider   ModelProvider
}

// NewSummaryMemory 创建摘要记忆
func NewSummaryMemory(truncMem *TruncationMemory, summaryProvider ModelProvider) *SummaryMemory {
	return &SummaryMemory{
		TruncationMemory: truncMem,
		summaryProvider:  summaryProvider,
	}
}

func (m *SummaryMemory) Compress(ctx context.Context, messages []Message) []Message {
	splitIdx := len(messages) / 2
	// 将 splitIdx 推进到真正的用户轮次边界（避免切在 tool_result 消息中间）
	for splitIdx < len(messages) && !isUserTurnBoundary(messages[splitIdx]) {
		splitIdx++
	}
	if splitIdx == 0 || splitIdx >= len(messages) {
		return m.TruncationMemory.Compress(ctx, messages)
	}

	// 把旧对话摘要化
	var summaryPrompt strings.Builder
	summaryPrompt.WriteString("用一两句话概括以下对话的核心信息：\n\n")
	for _, msg := range messages[:splitIdx] {
		for _, block := range msg.Content {
			if block.Type() == "text" {
				summaryPrompt.WriteString(block.Text())
				summaryPrompt.WriteString("\n")
			}
		}
	}

	resp, err := m.summaryProvider.Chat(ctx, &ChatRequest{
		Model:     "deepseek-v4-flash",
		MaxTokens: 512,
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{NewTextBlock(summaryPrompt.String())},
		}},
	})
	if err != nil {
		// 摘要失败，回退到截断
		return m.TruncationMemory.Compress(ctx, messages)
	}

	var summaryText string
	for _, block := range resp.Content {
		if block.Type() == "text" {
			summaryText = block.Text()
			break
		}
	}

	// 摘要 + 新对话
	return append([]Message{{
		Role:    "assistant",
		Content: []ContentBlock{NewTextBlock("【对话摘要】" + summaryText)},
	}}, messages[splitIdx:]...)
}
