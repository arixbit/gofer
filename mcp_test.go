package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/arixbit/gofer/agent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConnectMCPToolsUsesQualifiedNames(t *testing.T) {
	session := &fakeMCPSession{
		listResults: []*mcp.ListToolsResult{{
			Tools: []*mcp.Tool{{
				Name:        "forecast",
				Description: "查询天气预报",
				InputSchema: map[string]any{"type": "object"},
			}},
		}},
		callResult: &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "上海：晴"},
		}},
	}

	tools, closer, err := connectMCPToolsWithDialer(context.Background(), []MCPServerConfig{{
		Name: "weather",
		URL:  "https://example.test/mcp",
	}}, func(_ context.Context, config MCPServerConfig) (mcpToolSession, error) {
		if config.Name != "weather" {
			return nil, fmt.Errorf("unexpected server %q", config.Name)
		}
		return session, nil
	})
	if err != nil {
		t.Fatalf("connectMCPToolsWithDialer() error = %v", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	if len(tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(tools))
	}
	definition := tools[0].Definition()
	if definition.Name != "weather__forecast" {
		t.Fatalf("tool name = %q, want weather__forecast", definition.Name)
	}
	got, err := tools[0].Execute(context.Background(), json.RawMessage(`{"city":"上海"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got != "上海：晴" {
		t.Fatalf("Execute() = %q, want 上海：晴", got)
	}
	if session.callParams == nil || session.callParams.Name != "forecast" {
		t.Fatalf("CallTool params = %#v", session.callParams)
	}
	arguments, ok := session.callParams.Arguments.(map[string]any)
	if !ok || arguments["city"] != "上海" {
		t.Fatalf("CallTool arguments = %#v", session.callParams.Arguments)
	}
}

func TestConnectMCPToolsClosesOpenedSessionsOnLaterFailure(t *testing.T) {
	opened := &fakeMCPSession{listResults: []*mcp.ListToolsResult{{}}}
	_, _, err := connectMCPToolsWithDialer(context.Background(), []MCPServerConfig{
		{Name: "first", URL: "https://first.test/mcp"},
		{Name: "second", URL: "https://second.test/mcp"},
	}, func(_ context.Context, config MCPServerConfig) (mcpToolSession, error) {
		if config.Name == "second" {
			return nil, fmt.Errorf("dial failed")
		}
		return opened, nil
	})
	if err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("connectMCPToolsWithDialer() error = %v", err)
	}
	if opened.closeCalls != 1 {
		t.Fatalf("opened session close calls = %d, want 1", opened.closeCalls)
	}
}

func TestConnectMCPToolsReturnsCloserWithoutConfiguredServers(t *testing.T) {
	tools, closer, err := connectMCPToolsWithDialer(context.Background(), nil, func(context.Context, MCPServerConfig) (mcpToolSession, error) {
		t.Fatal("dialer should not be called when no MCP server is configured")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("connectMCPToolsWithDialer() error = %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("tool count = %d, want 0", len(tools))
	}
	if closer == nil {
		t.Fatal("closer is nil")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestMCPToolReturnsServerToolError(t *testing.T) {
	tool := &mcpTool{
		definition: agent.ToolDefinition{Name: "weather__forecast"},
		remoteName: "forecast",
		session: &fakeMCPSession{callResult: &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "城市不存在"}},
			IsError: true,
		}},
	}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"city":"火星"}`))
	if err == nil || !strings.Contains(err.Error(), "城市不存在") {
		t.Fatalf("Execute() error = %v, want MCP tool error", err)
	}
}

func TestMCPToolRejectsOversizedResult(t *testing.T) {
	tool := &mcpTool{
		definition: agent.ToolDefinition{Name: "large__result"},
		remoteName: "result",
		session: &fakeMCPSession{callResult: &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxMCPResultBytes+1)}},
		}},
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("Execute() error = %v, want size-limit error", err)
	}
}

type fakeMCPSession struct {
	listResults []*mcp.ListToolsResult
	listCalls   int
	callResult  *mcp.CallToolResult
	callErr     error
	callParams  *mcp.CallToolParams
	closeCalls  int
}

func (s *fakeMCPSession) ListTools(_ context.Context, _ *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	if s.listCalls >= len(s.listResults) {
		return &mcp.ListToolsResult{}, nil
	}
	result := s.listResults[s.listCalls]
	s.listCalls++
	return result, nil
}

func (s *fakeMCPSession) CallTool(_ context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	s.callParams = params
	return s.callResult, s.callErr
}

func (s *fakeMCPSession) Close() error {
	s.closeCalls++
	return nil
}
