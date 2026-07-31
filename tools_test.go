package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodingToolsOperateInWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	read := NewReadFileTool(workspace)
	if read.Definition().Name != "read" {
		t.Fatalf("read tool name = %q", read.Definition().Name)
	}
	got, err := read.Execute(context.Background(), json.RawMessage(`{"path":"hello.txt"}`))
	if err != nil || got != "hello world" {
		t.Fatalf("read = %q, %v", got, err)
	}

	write := NewWriteFileTool(workspace)
	if write.Definition().Name != "write" {
		t.Fatalf("write tool name = %q", write.Definition().Name)
	}
	if _, err := write.Execute(context.Background(), json.RawMessage(`{"path":"new.txt","content":"created"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := write.Execute(context.Background(), json.RawMessage(`{"path":"empty.txt","content":""}`)); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(content) != "created" {
		t.Fatalf("written content = %q, %v", content, err)
	}

	edit := NewEditFileTool(workspace)
	if edit.Definition().Name != "edit" {
		t.Fatalf("edit tool name = %q", edit.Definition().Name)
	}
	if _, err := edit.Execute(context.Background(), json.RawMessage(`{"path":"hello.txt","old_text":"world","new_text":"agent"}`)); err != nil {
		t.Fatalf("edit: %v", err)
	}
	content, err = os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil || string(content) != "hello agent" {
		t.Fatalf("edited content = %q, %v", content, err)
	}

	bash := NewBashTool(workspace)
	output, err := bash.Execute(context.Background(), json.RawMessage(`{"command":"pwd"}`))
	if err != nil || !strings.Contains(output, workspace.Root()) {
		t.Fatalf("bash workspace output = %q, %v", output, err)
	}
	output, err = bash.Execute(context.Background(), json.RawMessage(`{"command":"true"}`))
	if err != nil || output != "命令执行成功（退出码 0；无标准输出）。" {
		t.Fatalf("bash empty success output = %q, %v", output, err)
	}
}

func TestFileToolsRetainExplicitSafetyBoundary(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte("same same"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), bytes.Repeat([]byte("x"), defaultReadLimit+1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	read := NewReadFileTool(workspace)
	for _, input := range []string{
		`{"path":"../secret.txt"}`,
		`{"path":"` + filepath.Join(outside, "secret.txt") + `"}`,
		`{"path":"escape.txt"}`,
		`{"path":".env"}`,
		`{"path":"large.txt"}`,
	} {
		if _, err := read.Execute(context.Background(), json.RawMessage(input)); err == nil {
			t.Fatalf("read(%s) unexpectedly succeeded", input)
		}
	}

	edit := NewEditFileTool(workspace)
	if _, err := edit.Execute(context.Background(), json.RawMessage(`{"path":"many.txt","old_text":"same","new_text":"one"}`)); err == nil {
		t.Fatal("ambiguous edit unexpectedly succeeded")
	}
}
