package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolDefinition 工具元数据
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Tool 接口——定义和执行合一
type Tool interface {
	// Definition 返回工具的元数据（名字、描述、参数 schema）
	Definition() ToolDefinition

	// Execute 执行工具逻辑
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// ToolRegistry 工具注册表
type ToolRegistry struct {
	tools []Tool
	index map[string]Tool // name → tool 快速查找
}

// NewToolRegistry 创建工具注册表
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: []Tool{},
		index: make(map[string]Tool),
	}
}

// Register 注册工具
func (r *ToolRegistry) Register(tool Tool) error {
	def := tool.Definition()
	if _, exists := r.index[def.Name]; exists {
		return fmt.Errorf("工具 %q 已注册", def.Name)
	}
	r.tools = append(r.tools, tool)
	r.index[def.Name] = tool
	return nil
}

// Get 按名称获取工具
func (r *ToolRegistry) Get(name string) (Tool, error) {
	tool, ok := r.index[name]
	if !ok {
		return nil, fmt.Errorf("未知工具: %s", name)
	}
	return tool, nil
}

// List 返回所有已注册的工具
func (r *ToolRegistry) List() []Tool {
	return r.tools
}
