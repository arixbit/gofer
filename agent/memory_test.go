package agent

import (
	"context"
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
