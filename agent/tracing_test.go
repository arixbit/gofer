package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestConsoleTracerPrintsExecutionProgressWithoutConversationContent(t *testing.T) {
	var output bytes.Buffer
	tracer := NewConsoleTracer(&output)
	events := []TraceEvent{
		{Type: "run_start", Message: "这是用户的私密问题"},
		{Type: "model_request", Iteration: 1, Metadata: map[string]any{"message_count": 1, "tool_count": 4}},
		{Type: "model_response", Iteration: 1, StopReason: "tool_use", DurationMS: 12},
		{Type: "tool_call", Iteration: 1, ToolName: "write", Metadata: map[string]any{"input": `{"content":"不能泄露的内容"}`}},
		{Type: "tool_result", Iteration: 1, ToolName: "write", DurationMS: 3, Metadata: map[string]any{"result_len": 1024}},
		{Type: "model_response", Iteration: 2, StopReason: "end_turn", DurationMS: 8},
		{Type: "run_end", Iteration: 2, InputTokens: 24, OutputTokens: 9},
	}

	for _, event := range events {
		if err := tracer.Record(context.Background(), event); err != nil {
			t.Fatalf("Record(%s) error = %v", event.Type, err)
		}
	}

	got := output.String()
	for _, want := range []string{
		"[Runtime] 开始处理本次请求",
		"第 1 轮：请求模型（历史 1 条，可用工具 4 个）",
		"第 1 轮：模型请求工具（12ms）",
		"第 1 轮：调用工具 write",
		"第 1 轮：工具 write 完成（3ms）",
		"第 2 轮：模型给出最终回答（8ms）",
		"第 2 轮：完成（输入 24 tokens，输出 9 tokens）",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"这是用户的私密问题", "不能泄露的内容", "1024"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("output leaked %q:\n%s", forbidden, got)
		}
	}
}

func TestConsoleTracerIgnoresUnknownEvent(t *testing.T) {
	var output bytes.Buffer
	tracer := NewConsoleTracer(&output)
	if err := tracer.Record(context.Background(), TraceEvent{Type: "future_event"}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
}

func TestConsoleTracerDoesNotPrintErrorDetails(t *testing.T) {
	var output bytes.Buffer
	tracer := NewConsoleTracer(&output)
	events := []TraceEvent{
		{Type: "tool_result", Iteration: 1, ToolName: "bash", DurationMS: 4, Error: "命令输出包含 secret-value"},
		{Type: "run_error", Iteration: 2, Error: "provider 返回了 secret-value"},
	}
	for _, event := range events {
		if err := tracer.Record(context.Background(), event); err != nil {
			t.Fatalf("Record(%s) error = %v", event.Type, err)
		}
	}
	got := output.String()
	for _, want := range []string{"工具 bash 失败（4ms）", "第 2 轮：执行失败"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret-value") {
		t.Errorf("output leaked error detail:\n%s", got)
	}
}
