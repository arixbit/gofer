package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arixbit/gofer/agent"
)

func TestApplicationRegistersCustomToolAndCommand(t *testing.T) {
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{chat: func(request *agent.ChatRequest, call int) (*agent.ChatResponse, error) {
		switch call {
		case 1:
			if !hasTool(request.Tools, "custom_echo") {
				t.Fatal("custom tool was not included in model request")
			}
			return toolUse("echo_1", "custom_echo", `{"text":"hello"}`), nil
		case 2:
			if !hasToolResult(request.Messages, "echo_1") {
				t.Fatal("custom tool result was not returned to the model")
			}
			return &agent.ChatResponse{Content: []agent.ContentBlock{agent.NewTextBlock("完成")}, StopReason: "end_turn"}, nil
		default:
			return nil, fmt.Errorf("unexpected provider call %d", call)
		}
	}}
	application := newTestApplication(t, workspace, provider, &memorySessionStore{})
	if err := application.RegisterTool(&customEchoTool{}); err != nil {
		t.Fatalf("RegisterTool(): %v", err)
	}
	if err := application.RegisterCommand(Command{
		Name: "/local",
		Help: "本地测试命令",
		Run: func(context.Context, []string) (CommandResult, error) {
			return CommandResult{Output: "只在本地处理"}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterCommand(): %v", err)
	}

	output, _, handled, err := application.HandleLine(context.Background(), "/local")
	if err != nil || !handled || output != "只在本地处理" || provider.calls != 0 {
		t.Fatalf("custom command = output=%q handled=%v calls=%d err=%v", output, handled, provider.calls, err)
	}
	if _, err := application.Run(context.Background(), "调用自定义工具"); err != nil {
		t.Fatalf("Run(): %v", err)
	}
}

func TestDefaultToolOrderMatchesPi(t *testing.T) {
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	application := newTestApplication(t, workspace, &scriptedProvider{}, &memorySessionStore{})
	got := make([]string, 0, len(application.registry.List()))
	for _, tool := range application.registry.List() {
		got = append(got, tool.Definition().Name)
	}
	want := []string{"read", "bash", "edit", "write"}
	if !slices.Equal(got, want) {
		t.Fatalf("default tools = %v, want %v", got, want)
	}
}

func TestBuildSystemPromptIncludesWorkspaceInstructions(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("只改必要的文件。"), 0644); err != nil {
		t.Fatal(err)
	}
	prompt, err := buildSystemPrompt(workspace, "额外约束。")
	if err != nil {
		t.Fatalf("buildSystemPrompt(): %v", err)
	}
	for _, want := range []string{"只改必要的文件。", "额外约束。", "load_skill", "创建空文件", "删除文件时使用 bash", "不要重复同一项文件操作"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %q", want, prompt)
		}
	}
}

type customEchoTool struct {
}

func (t *customEchoTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name: "custom_echo",
		InputSchema: map[string]any{
			"type": "object",
		},
	}
}

func (t *customEchoTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	return args.Text, nil
}

func hasTool(tools []agent.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Definition().Name == name {
			return true
		}
	}
	return false
}
