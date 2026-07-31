package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arixbit/gofer/agent"
)

func TestCodingAgentRunsToolLoopAndRestoresSession(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewFileSessionStore(filepath.Join(root, ".coding-agent", "sessions"), root)
	provider := &scriptedProvider{chat: func(request *agent.ChatRequest, call int) (*agent.ChatResponse, error) {
		switch call {
		case 1:
			return toolUse("read_1", "read", `{"path":"hello.txt"}`), nil
		case 2:
			if !hasToolResult(request.Messages, "read_1") {
				t.Fatalf("second call is missing read result")
			}
			return toolUse("edit_1", "edit", `{"path":"hello.txt","old_text":"world","new_text":"agent"}`), nil
		case 3:
			if !hasToolResult(request.Messages, "edit_1") {
				t.Fatalf("third call is missing edit result")
			}
			return toolUse("test_1", "bash", `{"command":"test \"$(cat hello.txt)\" = \"hello agent\""}`), nil
		default:
			if !hasToolResult(request.Messages, "test_1") {
				t.Fatalf("final call is missing bash result")
			}
			return &agent.ChatResponse{Content: []agent.ContentBlock{agent.NewTextBlock("修改并验证完成。")}, StopReason: "end_turn"}, nil
		}
	}}
	application := newTestApplication(t, workspace, provider, store)
	var trace bytes.Buffer
	application.runtime = agent.NewAgent(
		provider,
		agent.WithToolRegistry(application.registry),
		agent.WithTracer(agent.NewJSONLTracer(&trace)),
	)
	answer, err := application.Run(context.Background(), "把 hello.txt 的 world 改成 agent，再验证结果")
	if err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if answer != "修改并验证完成。" || provider.calls != 4 {
		t.Fatalf("answer=%q calls=%d", answer, provider.calls)
	}
	t.Logf("runtime trace:\n%s", trace.String())
	content, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil || string(content) != "hello agent" {
		t.Fatalf("edited file = %q, %v", content, err)
	}

	restored, err := store.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if !hasToolResult(restored.Transcript, "edit_1") || !hasToolResult(restored.Transcript, "test_1") {
		t.Fatal("persisted session lost tool result IDs")
	}

	resumeProvider := &scriptedProvider{chat: func(request *agent.ChatRequest, call int) (*agent.ChatResponse, error) {
		if call != 1 || len(request.Messages) != len(restored.Context)+1 {
			t.Fatalf("resume received %d messages, want %d", len(request.Messages), len(restored.Context)+1)
		}
		return &agent.ChatResponse{Content: []agent.ContentBlock{agent.NewTextBlock("已接上上一轮。")}, StopReason: "end_turn"}, nil
	}}
	resumed := newTestApplication(t, workspace, resumeProvider, store)
	resumed.history = restored.Context
	resumed.transcript = restored.Transcript
	if _, err := resumed.Run(context.Background(), "继续"); err != nil {
		t.Fatalf("resumed Run(): %v", err)
	}
}

func toolUse(id, name, input string) *agent.ChatResponse {
	return &agent.ChatResponse{
		Content:    []agent.ContentBlock{agent.NewToolUseBlock(id, name, json.RawMessage(input))},
		StopReason: "tool_use",
	}
}

func hasToolResult(messages []agent.Message, id string) bool {
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type() == "tool_result" && block.ID() == id {
				return true
			}
		}
	}
	return false
}
