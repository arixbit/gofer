package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/gofer/agent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMCPDiscoveryTimeout      = 15 * time.Second
	defaultMCPToolCallTimeout       = 60 * time.Second
	defaultMCPResponseHeaderTimeout = 15 * time.Second
	maxMCPResultBytes               = 64 * 1024
)

type mcpToolSession interface {
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
	Close() error
}

type mcpSessionDialer func(ctx context.Context, config MCPServerConfig) (mcpToolSession, error)

// connectMCPTools opens the configured Streamable HTTP sessions and adapts
// every discovered remote tool into the local agent.Tool contract.
func connectMCPTools(ctx context.Context, configs []MCPServerConfig) ([]agent.Tool, io.Closer, error) {
	return connectMCPToolsWithDialer(ctx, configs, dialMCPServer)
}

func connectMCPToolsWithDialer(ctx context.Context, configs []MCPServerConfig, dialer mcpSessionDialer) ([]agent.Tool, io.Closer, error) {
	if dialer == nil {
		return nil, nil, fmt.Errorf("MCP dialer 不能为空")
	}

	connections := &mcpConnections{}
	if len(configs) == 0 {
		return nil, connections, nil
	}
	tools := make([]agent.Tool, 0)
	seenServers := make(map[string]struct{}, len(configs))
	seenTools := make(map[string]struct{})
	for _, config := range configs {
		if err := validateMCPServerConfig(config); err != nil {
			_ = connections.Close()
			return nil, nil, err
		}
		if _, exists := seenServers[config.Name]; exists {
			_ = connections.Close()
			return nil, nil, fmt.Errorf("MCP Server 名称 %q 重复", config.Name)
		}
		seenServers[config.Name] = struct{}{}

		session, err := dialer(ctx, config)
		if err != nil {
			_ = connections.Close()
			return nil, nil, fmt.Errorf("连接 MCP Server %q: %w", config.Name, err)
		}
		connections.sessions = append(connections.sessions, session)

		discoveryContext, cancelDiscovery := context.WithTimeout(ctx, defaultMCPDiscoveryTimeout)
		serverTools, err := discoverMCPTools(discoveryContext, config.Name, session)
		cancelDiscovery()
		if err != nil {
			_ = connections.Close()
			return nil, nil, fmt.Errorf("发现 MCP Server %q 的工具: %w", config.Name, err)
		}
		for _, tool := range serverTools {
			name := tool.Definition().Name
			if _, exists := seenTools[name]; exists {
				_ = connections.Close()
				return nil, nil, fmt.Errorf("MCP 工具名称 %q 重复", name)
			}
			seenTools[name] = struct{}{}
			tools = append(tools, tool)
		}
	}
	return tools, connections, nil
}

func validateMCPServerConfig(config MCPServerConfig) error {
	if strings.TrimSpace(config.Name) == "" {
		return fmt.Errorf("MCP Server 必须配置 name")
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return fmt.Errorf("MCP Server %q 的 URL 无效: %q", config.Name, config.URL)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return fmt.Errorf("MCP Server %q 的 URL 必须使用 http 或 https", config.Name)
	}
	return nil
}

func dialMCPServer(ctx context.Context, config MCPServerConfig) (mcpToolSession, error) {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "coding-agent",
		Version: "0.1.0",
	}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   config.URL,
		HTTPClient: newMCPHTTPClient(),
	}, nil)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func newMCPHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	clone := transport.Clone()
	clone.ResponseHeaderTimeout = defaultMCPResponseHeaderTimeout
	return &http.Client{Transport: clone}
}

func discoverMCPTools(ctx context.Context, serverName string, session mcpToolSession) ([]agent.Tool, error) {
	if session == nil {
		return nil, fmt.Errorf("MCP session 不能为空")
	}

	var (
		tools   []agent.Tool
		params  *mcp.ListToolsParams
		cursors = make(map[string]struct{})
	)
	for {
		result, err := session.ListTools(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("ListTools: %w", err)
		}
		if result == nil {
			return nil, fmt.Errorf("ListTools 返回空结果")
		}
		for _, remote := range result.Tools {
			if remote == nil {
				return nil, fmt.Errorf("ListTools 返回了空工具")
			}
			if strings.TrimSpace(remote.Name) == "" {
				return nil, fmt.Errorf("ListTools 返回了无名称工具")
			}
			schema, ok := remote.InputSchema.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("MCP 工具 %q 的输入结构不是 JSON 对象", remote.Name)
			}
			tools = append(tools, &mcpTool{
				definition: agent.ToolDefinition{
					Name:        qualifiedMCPToolName(serverName, remote.Name),
					Description: remote.Description,
					InputSchema: schema,
				},
				remoteName: remote.Name,
				session:    session,
			})
		}
		if result.NextCursor == "" {
			return tools, nil
		}
		if _, seen := cursors[result.NextCursor]; seen {
			return nil, fmt.Errorf("ListTools 返回了重复分页游标 %q", result.NextCursor)
		}
		cursors[result.NextCursor] = struct{}{}
		params = &mcp.ListToolsParams{Cursor: result.NextCursor}
	}
}

func qualifiedMCPToolName(serverName, toolName string) string {
	return serverName + "__" + toolName
}

type mcpTool struct {
	definition agent.ToolDefinition
	remoteName string
	session    mcpToolSession
}

func (t *mcpTool) Definition() agent.ToolDefinition {
	return t.definition
}

func (t *mcpTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if t.session == nil {
		return "", fmt.Errorf("MCP 工具 %q 没有可用会话", t.definition.Name)
	}
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage(`{}`)
	}
	arguments := make(map[string]any)
	if err := json.Unmarshal(input, &arguments); err != nil {
		return "", fmt.Errorf("解析 MCP 工具 %q 的参数: %w", t.definition.Name, err)
	}
	callContext, cancel := context.WithTimeout(ctx, defaultMCPToolCallTimeout)
	defer cancel()
	result, err := t.session.CallTool(callContext, &mcp.CallToolParams{
		Name:      t.remoteName,
		Arguments: arguments,
	})
	if err != nil {
		return "", fmt.Errorf("调用 MCP 工具 %q: %w", t.definition.Name, err)
	}
	if result == nil {
		return "", fmt.Errorf("MCP 工具 %q 返回空结果", t.definition.Name)
	}

	output, err := formatMCPToolResult(result)
	if err != nil {
		return "", fmt.Errorf("读取 MCP 工具 %q 的结果: %w", t.definition.Name, err)
	}
	if result.IsError {
		return "", fmt.Errorf("MCP 工具 %q 返回错误: %s", t.definition.Name, output)
	}
	return output, nil
}

func formatMCPToolResult(result *mcp.CallToolResult) (string, error) {
	parts := make([]string, 0, len(result.Content))
	size := 0
	for _, content := range result.Content {
		if content == nil {
			continue
		}
		var part string
		if text, ok := content.(*mcp.TextContent); ok {
			part = text.Text
		} else {
			encoded, err := json.Marshal(content)
			if err != nil {
				return "", fmt.Errorf("编码非文本结果: %w", err)
			}
			part = string(encoded)
		}
		separatorSize := 0
		if len(parts) > 0 {
			separatorSize = 1
		}
		if len(part)+separatorSize > maxMCPResultBytes-size {
			return "", fmt.Errorf("MCP 结果超过 %d 字节上限", maxMCPResultBytes)
		}
		size += len(part) + separatorSize
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n"), nil
}

type mcpConnections struct {
	mu       sync.Mutex
	sessions []mcpToolSession
	closed   bool
}

func (c *mcpConnections) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true

	errs := make([]error, 0)
	for i := len(c.sessions) - 1; i >= 0; i-- {
		if err := c.sessions[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
