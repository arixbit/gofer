package main

import (
	"context"
	"testing"

	"github.com/arixbit/gofer/agent"
)

func TestCommandsStayOutsideModelLoop(t *testing.T) {
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{chat: func(_ *agent.ChatRequest, _ int) (*agent.ChatResponse, error) {
		return &agent.ChatResponse{Content: []agent.ContentBlock{agent.NewTextBlock("模型回答")}, StopReason: "end_turn"}, nil
	}}
	application := newTestApplication(t, workspace, provider, &memorySessionStore{})

	output, exit, handled, err := application.HandleLine(context.Background(), "/history")
	if err != nil || !handled || exit || output != "当前可恢复上下文有 0 条消息；完整记录有 0 条消息。" {
		t.Fatalf("/history = output=%q exit=%v handled=%v err=%v", output, exit, handled, err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls after command = %d, want 0", provider.calls)
	}

	output, exit, handled, err = application.HandleLine(context.Background(), "你好")
	if err != nil || handled || exit || output != "模型回答" {
		t.Fatalf("prompt = output=%q exit=%v handled=%v err=%v", output, exit, handled, err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls after prompt = %d, want 1", provider.calls)
	}
}

func TestCommandRegistryAllowsCustomCommands(t *testing.T) {
	registry := NewCommandRegistry()
	if err := registry.Register(Command{
		Name: "/ping",
		Help: "测试自定义命令",
		Run: func(context.Context, []string) (CommandResult, error) {
			return CommandResult{Output: "pong"}, nil
		},
	}); err != nil {
		t.Fatalf("Register(): %v", err)
	}
	handled, result, err := registry.TryRun(context.Background(), "/ping")
	if err != nil || !handled || result.Output != "pong" {
		t.Fatalf("TryRun() = handled=%v result=%+v err=%v", handled, result, err)
	}
	if err := registry.Register(Command{Name: "/ping", Run: func(context.Context, []string) (CommandResult, error) { return CommandResult{}, nil }}); err == nil {
		t.Fatal("duplicate command unexpectedly registered")
	}
}
