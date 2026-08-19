package main

import (
	"context"
	"strings"
	"testing"

	"github.com/arixbit/gofer/agent"
)

func TestTranscriptSurvivesRuntimeContextCompression(t *testing.T) {
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &compressionProvider{t: t}
	store := &memorySessionStore{}
	// 注入小预算 memory，使 360002 字符的历史（约 180001 token）超过 180000 预算触发压缩。
	application := newTestApplication(t, workspace, provider, store,
		agent.WithMemory(agent.NewInMemoryMemory(180000)),
	)

	oldTurn := []agent.Message{
		{Role: "user", Content: []agent.ContentBlock{agent.NewTextBlock(strings.Repeat("x", 360002))}},
		{Role: "assistant", Content: []agent.ContentBlock{agent.NewTextBlock("旧回答")}},
	}
	application.history = append([]agent.Message(nil), oldTurn...)
	application.transcript = append([]agent.Message(nil), oldTurn...)

	if _, err := application.Run(context.Background(), "新的任务"); err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if len(application.history) != 2 {
		t.Fatalf("compressed context has %d messages, want current user and answer", len(application.history))
	}
	if len(application.transcript) != 4 {
		t.Fatalf("transcript has %d messages, want old and new turns", len(application.transcript))
	}
	if application.transcript[0].Content[0].Text() != oldTurn[0].Content[0].Text() {
		t.Fatal("transcript lost the early turn during context compression")
	}
	if len(store.state.Transcript) != len(application.transcript) {
		t.Fatal("stored transcript was not updated")
	}
}

type compressionProvider struct {
	t *testing.T
}

func (p *compressionProvider) Chat(_ context.Context, request *agent.ChatRequest) (*agent.ChatResponse, error) {
	if len(request.Messages) != 1 || request.Messages[0].Content[0].Text() != "新的任务" {
		p.t.Fatalf("model received uncompressed context: %#v", request.Messages)
	}
	return &agent.ChatResponse{
		Content:    []agent.ContentBlock{agent.NewTextBlock("新回答")},
		StopReason: "end_turn",
	}, nil
}

func (*compressionProvider) CountTokens(context.Context, []agent.Message) (int, error) {
	return 180000, nil
}
