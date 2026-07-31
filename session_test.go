package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arixbit/gofer/agent"
)

func TestFileSessionStoreRoundTripsToolCallPair(t *testing.T) {
	store := NewFileSessionStore(filepath.Join(t.TempDir(), "sessions"), "/workspace")
	want := SessionState{
		Context: []agent.Message{
			{Role: "user", Content: []agent.ContentBlock{agent.NewTextBlock("改一下文件")}},
			{Role: "assistant", Content: []agent.ContentBlock{
				agent.NewToolUseBlock("call_42", "edit_file", json.RawMessage(`{"path":"main.go","old_text":"old","new_text":"new"}`)),
			}},
			{Role: "user", Content: []agent.ContentBlock{agent.NewToolResultBlock("call_42", "已精确修改 main.go", false)}},
		},
	}
	want.Transcript = append([]agent.Message{{Role: "user", Content: []agent.ContentBlock{agent.NewTextBlock("更早的任务")}}}, want.Context...)
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions(): %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(sessions))
	}
	content, err := os.ReadFile(sessions[0].path)
	if err != nil {
		t.Fatalf("ReadFile(): %v", err)
	}
	if lines := bytes.Count(content, []byte("\n")); lines != 2 {
		t.Fatalf("session JSONL lines = %d, want 2", lines)
	}
	info, err := os.Stat(sessions[0].path)
	if err != nil {
		t.Fatalf("Stat(): %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("session permission = %o, want 0600", info.Mode().Perm())
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(got.Context) != len(want.Context) || len(got.Transcript) != len(want.Transcript) {
		t.Fatalf("loaded state = context %d/transcript %d, want %d/%d", len(got.Context), len(got.Transcript), len(want.Context), len(want.Transcript))
	}
	toolUse := got.Context[1].Content[0]
	if toolUse.Type() != "tool_use" || toolUse.ID() != "call_42" || toolUse.Name() != "edit_file" {
		t.Fatalf("tool_use not restored: type=%s id=%s name=%s", toolUse.Type(), toolUse.ID(), toolUse.Name())
	}
	var input map[string]string
	if err := json.Unmarshal(toolUse.Input(), &input); err != nil {
		t.Fatalf("tool_use input is not JSON: %v", err)
	}
	if input["path"] != "main.go" || input["old_text"] != "old" || input["new_text"] != "new" {
		t.Fatalf("tool_use input not restored: %#v", input)
	}
	toolResult := got.Context[2].Content[0]
	if toolResult.Type() != "tool_result" || toolResult.ID() != "call_42" || toolResult.Text() != "已精确修改 main.go" || toolResult.IsError() {
		t.Fatalf("tool_result not restored: id=%s text=%q error=%v", toolResult.ID(), toolResult.Text(), toolResult.IsError())
	}
}

func TestFileSessionStoreKeepsMultipleSessionsAndResumesOne(t *testing.T) {
	store := NewFileSessionStore(filepath.Join(t.TempDir(), "sessions"), "/workspace")
	first := SessionState{
		Context: []agent.Message{{Role: "user", Content: []agent.ContentBlock{agent.NewTextBlock("第一轮")}}},
	}
	if err := store.Save(first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions(): %v", err)
	}
	firstID := sessions[0].ID

	if _, err := store.NewSession(); err != nil {
		t.Fatalf("NewSession(): %v", err)
	}
	second := SessionState{
		Context: []agent.Message{{Role: "user", Content: []agent.ContentBlock{agent.NewTextBlock("第二轮")}}},
	}
	if err := store.Save(second); err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	sessions, err = store.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() after new session: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(sessions))
	}

	resumed, err := store.ResumeSession(firstID)
	if err != nil {
		t.Fatalf("ResumeSession(): %v", err)
	}
	if len(resumed.Context) != 1 || resumed.Context[0].Content[0].Text() != "第一轮" {
		t.Fatalf("resumed context = %#v, want first session", resumed.Context)
	}
	if store.CurrentSessionID() != firstID {
		t.Fatalf("current session ID = %q, want %q", store.CurrentSessionID(), firstID)
	}
}
