package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/gofer/agent"
)

const defaultReadLimit = 64 * 1024

type ReadFileTool struct {
	workspace Workspace
	maxBytes  int
}

func NewReadFileTool(workspace Workspace) *ReadFileTool {
	return &ReadFileTool{workspace: workspace, maxBytes: defaultReadLimit}
}

func (t *ReadFileTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "read",
		Description: "读取 workspace 内一个相对路径的文本文件，最多返回 64 KiB。",
		InputSchema: pathSchema(),
	}
}

func (t *ReadFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("解析 read 参数: %w", err)
	}
	path, err := t.workspace.ResolveRead(args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("读取 %q: %w", args.Path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("拒绝读取非普通文件 %q", args.Path)
	}
	if info.Size() > int64(t.maxBytes) {
		return "", fmt.Errorf("文件 %q 超过 %d 字节读取上限", args.Path, t.maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取 %q: %w", args.Path, err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, int64(t.maxBytes)+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", fmt.Errorf("读取 %q: %w", args.Path, readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("关闭 %q: %w", args.Path, closeErr)
	}
	if len(content) > t.maxBytes {
		return "", fmt.Errorf("文件 %q 超过 %d 字节读取上限", args.Path, t.maxBytes)
	}
	return string(content), nil
}

type WriteFileTool struct {
	workspace Workspace
}

func NewWriteFileTool(workspace Workspace) *WriteFileTool {
	return &WriteFileTool{workspace: workspace}
}

func (t *WriteFileTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "write",
		Description: "覆盖或新建 workspace 内一个相对路径的文本文件。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "相对于 workspace 的文件路径"},
				"content": map[string]any{"type": "string", "description": "要写入的完整文本"},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
	}
}

func (t *WriteFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("解析 write 参数: %w", err)
	}
	path, err := t.workspace.ResolveWrite(args.Path)
	if err != nil {
		return "", err
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}
	if err := atomicWrite(path, []byte(args.Content), mode); err != nil {
		return "", fmt.Errorf("写入 %q: %w", args.Path, err)
	}
	return fmt.Sprintf("已写入 %s（%d 字节）", args.Path, len(args.Content)), nil
}

type EditFileTool struct {
	workspace Workspace
}

func NewEditFileTool(workspace Workspace) *EditFileTool {
	return &EditFileTool{workspace: workspace}
}

func (t *EditFileTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "edit",
		Description: "在 workspace 内精确替换文件中的一段文本。old_text 必须恰好出现一次。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":     map[string]any{"type": "string", "description": "相对于 workspace 的文件路径"},
				"old_text": map[string]any{"type": "string", "description": "文件中唯一出现的原文本"},
				"new_text": map[string]any{"type": "string", "description": "替换后的文本"},
			},
			"required":             []string{"path", "old_text", "new_text"},
			"additionalProperties": false,
		},
	}
}

func (t *EditFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("解析 edit 参数: %w", err)
	}
	if args.OldText == "" {
		return "", fmt.Errorf("old_text 不能为空")
	}
	path, err := t.workspace.ResolveWrite(args.Path)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 %q: %w", args.Path, err)
	}
	count := strings.Count(string(content), args.OldText)
	if count != 1 {
		return "", fmt.Errorf("拒绝编辑 %q：old_text 出现 %d 次，必须恰好一次", args.Path, count)
	}
	updated := strings.Replace(string(content), args.OldText, args.NewText, 1)
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}
	if err := atomicWrite(path, []byte(updated), mode); err != nil {
		return "", fmt.Errorf("写入 %q: %w", args.Path, err)
	}
	return fmt.Sprintf("已精确修改 %s", args.Path), nil
}

type BashTool struct {
	workspace   Workspace
	timeout     time.Duration
	outputLimit int
}

func NewBashTool(workspace Workspace) *BashTool {
	return &BashTool{
		workspace:   workspace,
		timeout:     15 * time.Second,
		outputLimit: 64 * 1024,
	}
}

func (t *BashTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "bash",
		Description: "在 workspace 根目录运行一条 Bash 命令。它不是沙箱：命令以启动 Agent 的用户权限执行；默认只等待 Bash 主进程 15 秒，不能约束后台子进程。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":         map[string]any{"type": "string", "description": "要运行的 Bash 命令"},
				"timeout_seconds": map[string]any{"type": "integer", "description": "超时时间（秒，最大 60）"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("解析 bash 参数: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command 不能为空")
	}
	timeout := t.timeout
	if args.TimeoutSeconds != 0 {
		if args.TimeoutSeconds < 1 || args.TimeoutSeconds > 60 {
			return "", fmt.Errorf("timeout_seconds 必须在 1 到 60 之间")
		}
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}

	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Bash inherits the launching process's user permissions; setting Dir only
	// selects its starting directory and must not be mistaken for a sandbox.
	command := exec.CommandContext(commandContext, "bash", "-lc", args.Command)
	command.Dir = t.workspace.Root()
	output := &limitedBuffer{limit: t.outputLimit}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	result := output.String()
	if output.Truncated() {
		result += "\n[输出已截断]"
	}
	if commandContext.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("命令在 %s 后超时", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("命令执行失败: %w", err)
	}
	if result == "" {
		return "命令执行成功（退出码 0；无标准输出）。", nil
	}
	return result, nil
}

func pathSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "相对于 workspace 的文件路径"},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(content), nil
	}
	if len(content) > remaining {
		_, _ = b.buffer.Write(content[:remaining])
		b.truncated = true
		return len(content), nil
	}
	_, _ = b.buffer.Write(content)
	return len(content), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
