package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type loopProvider struct {
	calls int
}

func (p *loopProvider) Chat(_ context.Context, req *ChatRequest) (*ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &ChatResponse{
			Content: []ContentBlock{
				NewToolUseBlock("call_1", "echo", json.RawMessage(`{"text":"hello"}`)),
			},
			StopReason: "tool_use",
		}, nil
	}

	if !containsToolResult(req.Messages, "call_1", "hello") {
		return nil, fmt.Errorf("第二次模型请求缺少工具结果")
	}
	return &ChatResponse{
		Content:    []ContentBlock{NewTextBlock("完成")},
		StopReason: "end_turn",
	}, nil
}

func (p *loopProvider) CountTokens(_ context.Context, _ []Message) (int, error) {
	return 10, nil
}

type echoTool struct {
}

func (t *echoTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "echo"}
}

func (t *echoTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("解析 echo 参数: %w", err)
	}
	return args.Text, nil
}

func TestAgentRunExecutesToolAndReturnsFinalAnswer(t *testing.T) {
	provider := &loopProvider{}
	registry := NewToolRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	runtime := NewAgent(provider,
		WithToolRegistry(registry),
	)
	resp, err := runtime.Run(context.Background(), Request{Message: "say hello"})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp.Text != "完成" {
		t.Fatalf("Run().Text = %q, want %q", resp.Text, "完成")
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
}

func TestAgentRunContinuesPastFormerIterationLimit(t *testing.T) {
	provider := &manyToolCallsProvider{remaining: 17}
	registry := NewToolRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	response, err := NewAgent(provider, WithToolRegistry(registry)).Run(context.Background(), Request{Message: "repeat"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if response.Text != "完成" || provider.calls != 18 {
		t.Fatalf("response=%q calls=%d, want completed response after 18 calls", response.Text, provider.calls)
	}
}

func TestAgentRunStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err := NewAgent(&loopProvider{}).Run(ctx, Request{Message: "stop"})
	if err == nil {
		t.Fatal("Run() error = nil, want cancellation error")
	}
	if response == nil || len(response.History) != 1 {
		t.Fatalf("partial response = %#v, want the user message preserved", response)
	}
}

type manyToolCallsProvider struct {
	calls     int
	remaining int
}

func (p *manyToolCallsProvider) Chat(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
	p.calls++
	if p.remaining == 0 {
		return &ChatResponse{Content: []ContentBlock{NewTextBlock("完成")}, StopReason: "end_turn"}, nil
	}
	p.remaining--
	return &ChatResponse{Content: []ContentBlock{NewToolUseBlock(fmt.Sprintf("call_%d", p.calls), "echo", json.RawMessage(`{"text":"hello"}`))}, StopReason: "tool_use"}, nil
}

func (*manyToolCallsProvider) CountTokens(_ context.Context, _ []Message) (int, error) {
	return 10, nil
}

type failingAfterToolProvider struct {
	calls int
}

func (p *failingAfterToolProvider) Chat(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &ChatResponse{
			Content:    []ContentBlock{NewToolUseBlock("call_1", "echo", json.RawMessage(`{"text":"hello"}`))},
			StopReason: "tool_use",
		}, nil
	}
	return nil, fmt.Errorf("provider unavailable")
}

func (p *failingAfterToolProvider) CountTokens(_ context.Context, _ []Message) (int, error) {
	return 10, nil
}

func TestAgentRunReturnsPartialHistoryWhenProviderFails(t *testing.T) {
	provider := &failingAfterToolProvider{}
	registry := NewToolRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	runtime := NewAgent(provider,
		WithToolRegistry(registry),
	)
	response, err := runtime.Run(context.Background(), Request{Message: "say hello"})
	if err == nil {
		t.Fatal("Run() error = nil, want provider error")
	}
	if response == nil {
		t.Fatal("Run() response = nil, want partial history")
	}
	if !containsToolResult(response.History, "call_1", "hello") {
		t.Fatalf("partial history lost tool result: %#v", response.History)
	}
}

func containsToolResult(messages []Message, id, text string) bool {
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.Type() == "tool_result" && block.ID() == id && block.Text() == text {
				return true
			}
		}
	}
	return false
}
