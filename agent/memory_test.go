package agent

import (
	"context"
	"strings"
	"testing"
)

func TestInMemoryMemory_Compress_RespectsUserTurnBoundary(t *testing.T) {
	// 构造：user(text) → assistant(tool_use) → user(tool_result) → assistant(text) → user(text)
	// 压缩后第一条不应是 tool_result（会被 provider 转成孤立 role:tool）
	history := []Message{
		{Role: "user", Content: []ContentBlock{NewTextBlock("hello")}},
		{Role: "assistant", Content: []ContentBlock{
			NewToolUseBlock("call_1", "search", nil),
		}},
		{Role: "user", Content: []ContentBlock{
			NewToolResultBlock("call_1", "found 3 results", false),
		}},
		{Role: "assistant", Content: []ContentBlock{NewTextBlock("here are the results")}},
		{Role: "user", Content: []ContentBlock{NewTextBlock("thanks")}},
	}

	// 设置极低预算，强制至少删除第一轮
	mem := NewInMemoryMemory(1)
	compressed := mem.Compress(context.Background(), history)

	if len(compressed) == 0 {
		t.Fatal("压缩后消息列表为空")
	}

	// 第一条消息不应是纯 tool_result
	first := compressed[0]
	if first.Role == "user" {
		hasToolResult := false
		hasText := false
		for _, b := range first.Content {
			switch b.Type() {
			case "tool_result":
				hasToolResult = true
			case "text":
				hasText = true
			}
		}
		if hasToolResult && !hasText {
			t.Errorf("截断后第一条消息是孤立 tool_result，会被 provider 转成无对应 tool_calls 的 role:tool 消息，导致 API 400")
		}
	}
}

func Test_isUserTurnBoundary(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want bool
	}{
		{"纯文本 user", Message{Role: "user", Content: []ContentBlock{NewTextBlock("hi")}}, true},
		{"纯 tool_result user", Message{Role: "user", Content: []ContentBlock{NewToolResultBlock("id", "result", false)}}, false},
		{"文本+tool_result user", Message{Role: "user", Content: []ContentBlock{NewTextBlock("hi"), NewToolResultBlock("id", "r", false)}}, true},
		{"assistant", Message{Role: "assistant", Content: []ContentBlock{NewTextBlock("hi")}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUserTurnBoundary(tt.msg); got != tt.want {
				t.Errorf("isUserTurnBoundary() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestInMemoryMemory_CompressAndShouldCompressShareEstimation 验证 ShouldCompress 和 Compress
// 使用同一套 token 估算口径：一旦 ShouldCompress 报 true，Compress 后的结果必须报 false。
// 这正是"240 万 token 压不下去"那个 bug 的回归守卫——两套口径不一致会导致触发后压不动。
func TestInMemoryMemory_CompressAndShouldCompressShareEstimation(t *testing.T) {
	// 构造多轮历史：前两轮各 400 字符（估算 200 token），最后一轮在预算内。
	// 预算 150：前两轮加起来超预算触发压缩，删掉后只剩最后一轮，不再触发。
	history := []Message{
		{Role: "user", Content: []ContentBlock{NewTextBlock(strings.Repeat("a", 400))}},
		{Role: "assistant", Content: []ContentBlock{NewTextBlock("ok")}},
		{Role: "user", Content: []ContentBlock{NewTextBlock(strings.Repeat("b", 400))}},
		{Role: "assistant", Content: []ContentBlock{NewTextBlock("ok")}},
		{Role: "user", Content: []ContentBlock{NewTextBlock("last")}},
	}
	mem := NewInMemoryMemory(150)

	if !mem.ShouldCompress(history) {
		t.Fatal("ShouldCompress(history) = false, want true（历史超出预算）")
	}
	compressed := mem.Compress(context.Background(), history)
	if mem.ShouldCompress(compressed) {
		t.Fatalf("Compress 后仍触发压缩：口径不一致，压缩结果 %d 条消息", len(compressed))
	}
}

// TestInMemoryMemory_EstimateTokensCountsReasoning 验证 reasoning 内容被计入 token 估算，
// 否则开思考后每轮累积的推理内容会被估成 0，压缩触发远晚于实际需要。
func TestInMemoryMemory_EstimateTokensCountsReasoning(t *testing.T) {
	withReasoning := []Message{
		{Role: "assistant", Content: []ContentBlock{NewReasoningBlock(strings.Repeat("r", 400))}},
	}
	withoutReasoning := []Message{
		{Role: "assistant", Content: []ContentBlock{NewTextBlock("ok")}},
	}
	if estimateTokens(withReasoning) == estimateTokens(withoutReasoning) {
		t.Fatal("reasoning 块未被计入 token 估算，开思考后压缩会延迟")
	}
}
