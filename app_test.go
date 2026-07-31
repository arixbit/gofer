package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arixbit/gofer/agent"
)

func TestBuildApplicationWithTracerShowsRuntimeProgress(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile(hello.txt): %v", err)
	}
	var trace bytes.Buffer
	application, closer, err := buildApplicationWithConnectorAndTracer(
		context.Background(),
		Config{
			Workspace:  root,
			SessionDir: filepath.Join(root, ".coding-agent", "sessions"),
			Provider:   ProviderConfig{Type: "deepseek", Model: "test"},
		},
		func(ProviderConfig) (agent.ModelProvider, error) {
			return &scriptedProvider{chat: func(_ *agent.ChatRequest, call int) (*agent.ChatResponse, error) {
				if call == 1 {
					return toolUse("read_1", "read", `{"path":"hello.txt"}`), nil
				}
				return &agent.ChatResponse{
					Content:    []agent.ContentBlock{agent.NewTextBlock("完成")},
					StopReason: "end_turn",
				}, nil
			}}, nil
		},
		func(context.Context, []MCPServerConfig) ([]agent.Tool, io.Closer, error) {
			return nil, &mcpConnections{}, nil
		},
		agent.NewConsoleTracer(&trace),
	)
	if err != nil {
		t.Fatalf("buildApplicationWithConnectorAndTracer() error = %v", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	if _, err := application.Run(context.Background(), "不要在 trace 中打印这句话"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := trace.String(); !strings.Contains(got, "[Runtime] 第 2 轮：完成") {
		t.Fatalf("trace = %q, want runtime completion", got)
	} else if strings.Contains(got, "不要在 trace 中打印这句话") {
		t.Fatalf("trace leaked user message: %q", got)
	}
	t.Logf("console trace:\n%s", trace.String())
}

func TestBuildApplicationKeepsMCPConnectionContextAlive(t *testing.T) {
	root := t.TempDir()
	var connectionContext context.Context
	application, closer, err := buildApplicationWithConnector(
		context.Background(),
		Config{
			Workspace:  root,
			SessionDir: root + "/sessions",
			Provider:   ProviderConfig{Type: "deepseek", Model: "test"},
			MCPServers: []MCPServerConfig{{Name: "test", URL: "https://example.test/mcp"}},
		},
		func(ProviderConfig) (agent.ModelProvider, error) {
			return &scriptedProvider{chat: func(*agent.ChatRequest, int) (*agent.ChatResponse, error) {
				return &agent.ChatResponse{StopReason: "end_turn"}, nil
			}}, nil
		},
		func(ctx context.Context, _ []MCPServerConfig) ([]agent.Tool, io.Closer, error) {
			connectionContext = ctx
			return nil, &mcpConnections{}, nil
		},
	)
	if err != nil {
		t.Fatalf("buildApplicationWithConnector(): %v", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	if application == nil || connectionContext == nil {
		t.Fatal("application or MCP connection context is nil")
	}
	if err := connectionContext.Err(); err != nil {
		t.Fatalf("MCP connection context was cancelled after startup: %v", err)
	}
}

func TestBuildApplicationStartsNewSessionAndRequiresExplicitResume(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	previousStore := NewFileSessionStore(sessionDir, root)
	previousState := SessionState{
		Context: []agent.Message{{
			Role:    "user",
			Content: []agent.ContentBlock{agent.NewTextBlock("创建快速排序 hello.go")},
		}},
	}
	if err := previousStore.Save(previousState); err != nil {
		t.Fatalf("Save(previousState) error: %v", err)
	}
	previousSessions, err := previousStore.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	previousID := previousSessions[0].ID

	application, closer, err := buildApplicationWithConnector(
		context.Background(),
		Config{
			Workspace:  root,
			SessionDir: sessionDir,
			Provider:   ProviderConfig{Type: "deepseek", Model: "test"},
		},
		func(ProviderConfig) (agent.ModelProvider, error) {
			return &scriptedProvider{chat: func(*agent.ChatRequest, int) (*agent.ChatResponse, error) {
				return &agent.ChatResponse{StopReason: "end_turn"}, nil
			}}, nil
		},
		func(context.Context, []MCPServerConfig) ([]agent.Tool, io.Closer, error) {
			return nil, &mcpConnections{}, nil
		},
	)
	if err != nil {
		t.Fatalf("buildApplicationWithConnector() error: %v", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	}()

	if len(application.history) != 0 || len(application.transcript) != 0 {
		t.Fatalf("startup restored previous context: history=%d transcript=%d", len(application.history), len(application.transcript))
	}
	catalog, ok := application.session.(SessionCatalog)
	if !ok {
		t.Fatal("application session does not support the session catalog")
	}
	sessions, err := catalog.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() after startup error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("startup session count = %d, want 1 existing session and no new file", len(sessions))
	}
	output, exit, handled, err := application.HandleLine(context.Background(), "/resume "+previousID)
	if err != nil || !handled || exit || !strings.Contains(output, previousID) {
		t.Fatalf("/resume = output=%q exit=%v handled=%v err=%v", output, exit, handled, err)
	}
	if len(application.history) != 1 || application.history[0].Content[0].Text() != "创建快速排序 hello.go" {
		t.Fatalf("explicit resume history = %#v, want previous context", application.history)
	}
	output, exit, handled, err = application.HandleLine(context.Background(), "/new")
	if err != nil || !handled || exit || !strings.Contains(output, "已创建新会话") {
		t.Fatalf("/new = output=%q exit=%v handled=%v err=%v", output, exit, handled, err)
	}
	if len(application.history) != 0 || len(application.transcript) != 0 {
		t.Fatalf("/new retained previous context: history=%d transcript=%d", len(application.history), len(application.transcript))
	}
	if catalog.CurrentSessionID() == previousID {
		t.Fatal("/new did not switch the current session")
	}
}

func TestRunREPLAnnouncesNewSession(t *testing.T) {
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	application := newTestApplication(t, workspace, &scriptedProvider{}, &memorySessionStore{})
	var output strings.Builder
	if err := runREPL(context.Background(), application, bufio.NewReader(strings.NewReader("")), &output); err != nil {
		t.Fatalf("runREPL() error: %v", err)
	}
	if !strings.Contains(output.String(), "已开始新会话") {
		t.Fatalf("startup output = %q, want new-session notice", output.String())
	}
}

func TestApplicationPrintsStreamingTextOnlyOnce(t *testing.T) {
	application := &Application{
		runtime: streamingTestAgent{},
		session: &memorySessionStore{},
	}
	var output strings.Builder
	application.SetStreamOutput(&output)

	result, err := application.Run(context.Background(), "你好")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "" {
		t.Fatalf("Run() result = %q, want empty after streaming", result)
	}
	if output.String() != "实时答案\n" {
		t.Fatalf("stream output = %q, want one streamed answer", output.String())
	}
}

type streamingTestAgent struct{}

func (streamingTestAgent) Run(ctx context.Context, request agent.Request) (*agent.Response, error) {
	for _, event := range []agent.StreamEvent{
		{Type: agent.StreamEventStart, Iteration: 1},
		{Type: agent.StreamEventTextDelta, Iteration: 1, Text: "实时"},
		{Type: agent.StreamEventTextDelta, Iteration: 1, Text: "答案"},
		{Type: agent.StreamEventDone, Iteration: 1, StopReason: "end_turn"},
	} {
		if request.StreamSink != nil {
			if err := request.StreamSink(ctx, event); err != nil {
				return nil, err
			}
		}
	}
	return &agent.Response{
		Text: "实时答案",
		History: []agent.Message{{
			Role:    "user",
			Content: []agent.ContentBlock{agent.NewTextBlock(request.Message)},
		}},
	}, nil
}

func TestApplicationPersistsPartialToolHistoryWhenProviderFails(t *testing.T) {
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &memorySessionStore{}
	provider := &scriptedProvider{chat: func(_ *agent.ChatRequest, call int) (*agent.ChatResponse, error) {
		if call == 1 {
			return toolUse("write_1", "write", `{"path":"partial.txt","content":"created"}`), nil
		}
		return nil, fmt.Errorf("provider unavailable")
	}}
	application := newTestApplication(t, workspace, provider, store)

	if _, err := application.Run(context.Background(), "创建 partial.txt"); err == nil {
		t.Fatal("Run() error = nil, want provider error")
	}
	if !hasToolResult(store.state.Context, "write_1") {
		t.Fatalf("saved context lost tool result: %#v", store.state.Context)
	}
	content, err := os.ReadFile(filepath.Join(workspace.Root(), "partial.txt"))
	if err != nil || string(content) != "created" {
		t.Fatalf("partial.txt = %q, %v", content, err)
	}
}

func TestApplicationWritesSameFileTwiceWithoutPromptState(t *testing.T) {
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{chat: func(request *agent.ChatRequest, call int) (*agent.ChatResponse, error) {
		switch call {
		case 1:
			return toolUse("write_1", "write", `{"path":"answer.go","content":"package answer"}`), nil
		case 2:
			return toolUse("write_2", "write", `{"path":"./answer.go","content":"package answer\n\nfunc Answer() int { return 42 }"}`), nil
		case 3:
			return &agent.ChatResponse{Content: []agent.ContentBlock{agent.NewTextBlock("完成")}, StopReason: "end_turn"}, nil
		default:
			return nil, fmt.Errorf("unexpected provider call %d", call)
		}
	}}
	application := newTestApplication(t, workspace, provider, &memorySessionStore{})

	if _, err := application.Run(context.Background(), "创建 answer.go 后补函数"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls = %d, want 3", provider.calls)
	}
	content, err := os.ReadFile(filepath.Join(workspace.Root(), "answer.go"))
	if err != nil || !strings.Contains(string(content), "func Answer") {
		t.Fatalf("answer.go = %q, %v", content, err)
	}
}
