package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Request 是一次 Agent 调用的输入
type Request struct {
	// 用户当前的问题
	Message string
	// 之前的对话历史（由框架管理）
	History []Message
	// System prompt
	System string
	// StreamSink 接收模型流事件；为空时仍会完整执行流式请求，但不向调用方推送事件。
	StreamSink StreamSink
}

// Response 是 Agent 调用的输出
type Response struct {
	// 最终回答文本
	Text string
	// 更新后的对话历史
	History []Message
	// 消耗 token 统计
	InputTokens  int
	OutputTokens int
}

// Message 是对话中的一条消息
type Message struct {
	Role string // "user" / "assistant"
	// Content 按顺序保存文本、tool_use 或 tool_result 内容块。
	Content []ContentBlock
}

// ContentBlock 是消息内容块
type ContentBlock interface {
	Type() string // "text" / "tool_use" / "tool_result" / "reasoning"
	// 以下方法仅特定类型的 Block 实现，调用前应先用 Type() 判断
	ID() string             // tool_use / tool_result
	Name() string           // tool_use
	Input() json.RawMessage // tool_use
	Text() string           // text
	IsError() bool          // tool_result
	Reasoning() string      // reasoning
}

// NewTextBlock 创建文本内容块
func NewTextBlock(text string) ContentBlock {
	return &textBlock{text: text}
}

// NewToolResultBlock 创建工具结果内容块
// id 必须与对应的 tool_use 调用 ID 相同；isError 表示工具执行是否失败。
func NewToolResultBlock(id string, result string, isError bool) ContentBlock {
	return &toolResultBlock{id: id, result: result, isError: isError}
}

// NewToolUseBlock 创建工具调用内容块
// 由 ModelProvider 解析 API 响应时构造，框架使用者一般不需要直接调用
func NewToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return &toolUseBlock{id: id, name: name, input: input}
}

// NewReasoningBlock 创建思考内容块。
// DeepSeek 等 reasoner 模型会把推理过程放在 reasoning_content，框架将其存为 reasoning 块，
// 在下一轮请求中原样回传，满足多轮工具调用对 reasoning_content 的回传约束。
func NewReasoningBlock(reasoning string) ContentBlock {
	return &reasoningBlock{reasoning: reasoning}
}

type textBlock struct {
	text string
}

func (b *textBlock) Type() string           { return "text" }
func (b *textBlock) Text() string           { return b.text }
func (b *textBlock) ID() string             { return "" }
func (b *textBlock) Name() string           { return "" }
func (b *textBlock) Input() json.RawMessage { return nil }
func (b *textBlock) IsError() bool          { return false }
func (b *textBlock) Reasoning() string      { return "" }

type toolResultBlock struct {
	id      string
	result  string
	isError bool
}

func (b *toolResultBlock) Type() string           { return "tool_result" }
func (b *toolResultBlock) ID() string             { return b.id }
func (b *toolResultBlock) Name() string           { return "" }
func (b *toolResultBlock) Input() json.RawMessage { return nil }
func (b *toolResultBlock) Text() string           { return b.result }
func (b *toolResultBlock) IsError() bool          { return b.isError }
func (b *toolResultBlock) Reasoning() string      { return "" }

type toolUseBlock struct {
	id    string
	name  string
	input json.RawMessage
}

func (b *toolUseBlock) Type() string           { return "tool_use" }
func (b *toolUseBlock) ID() string             { return b.id }
func (b *toolUseBlock) Name() string           { return b.name }
func (b *toolUseBlock) Input() json.RawMessage { return b.input }
func (b *toolUseBlock) Text() string           { return "" }
func (b *toolUseBlock) IsError() bool          { return false }
func (b *toolUseBlock) Reasoning() string      { return "" }

type reasoningBlock struct {
	reasoning string
}

func (b *reasoningBlock) Type() string           { return "reasoning" }
func (b *reasoningBlock) Reasoning() string      { return b.reasoning }
func (b *reasoningBlock) Text() string           { return "" }
func (b *reasoningBlock) ID() string             { return "" }
func (b *reasoningBlock) Name() string           { return "" }
func (b *reasoningBlock) Input() json.RawMessage { return nil }
func (b *reasoningBlock) IsError() bool          { return false }

// Agent 是框架的核心接口
type Agent interface {
	// Run 执行一次 Agent 决策循环
	Run(ctx context.Context, req Request) (*Response, error)
}

// AgentOption 函数式配置
type AgentOption func(*agentConfig)

// DeepSeek 官方规格：上下文长度 1M tokens，单轮输出最大 384K tokens。
// 上下文预算留出输出空间，设为 640K（略低于上限，给系统提示和工具定义留安全边界）。
const (
	defaultContextBudget = 640 * 1024 // 上下文压缩预算
	defaultMaxTokens     = 32 * 1024 // 单轮输出上限（保留 reasoning + 正文空间）
)

type agentConfig struct {
	maxTokens    int
	systemPrompt string
	tools        []Tool
	memory       Memory
	registry     *ToolRegistry
	tracer       Tracer
}

func defaultConfig() *agentConfig {
	return &agentConfig{
		maxTokens: defaultMaxTokens,
		tools:     []Tool{},
		memory:    NewInMemoryMemory(defaultContextBudget),
		tracer:    NoopTracer{},
	}
}

func WithMaxTokens(n int) AgentOption {
	return func(c *agentConfig) { c.maxTokens = n }
}

func WithSystemPrompt(prompt string) AgentOption {
	return func(c *agentConfig) { c.systemPrompt = prompt }
}

func WithTools(tools ...Tool) AgentOption {
	return func(c *agentConfig) { c.tools = tools }
}

func WithMemory(m Memory) AgentOption {
	return func(c *agentConfig) { c.memory = m }
}

func WithToolRegistry(registry *ToolRegistry) AgentOption {
	return func(c *agentConfig) { c.registry = registry }
}

// WithTracer 配置 Agent 运行过程的事件记录器。
func WithTracer(tracer Tracer) AgentOption {
	return func(c *agentConfig) {
		if tracer == nil {
			c.tracer = NoopTracer{}
			return
		}
		c.tracer = tracer
	}
}

// reactAgent 是 Agent 接口的默认实现
type reactAgent struct {
	provider ModelProvider
	config   *agentConfig
}

// NewAgent 创建 Agent 实例
func NewAgent(provider ModelProvider, opts ...AgentOption) Agent {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	return &reactAgent{
		provider: provider,
		config:   cfg,
	}
}

func (a *reactAgent) Run(ctx context.Context, req Request) (*Response, error) {
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	a.trace(ctx, TraceEvent{
		RunID:   runID,
		Type:    "run_start",
		Message: req.Message,
	})

	messages := append(req.History, Message{
		Role:    "user",
		Content: []ContentBlock{NewTextBlock(req.Message)},
	})

	totalInput, totalOutput := 0, 0

	// Pi 风格 Runtime 不设固定工具轮数；停止边界由调用方取消和应用层超时决定。
	for iteration := 1; ; iteration++ {
		if err := ctx.Err(); err != nil {
			wrapped := fmt.Errorf("Agent 运行被取消: %w", err)
			a.trace(ctx, TraceEvent{RunID: runID, Type: "run_error", Iteration: iteration, Error: wrapped.Error()})
			return partialResponse(messages, totalInput, totalOutput), wrapped
		}
		// token 预算检查：Memory 自行估算，和 Compress 使用同一套口径，保证触发后一定能压到预算内。
		if a.config.memory.ShouldCompress(messages) {
			messages = a.config.memory.Compress(ctx, messages)
		}

		// 收集可用工具
		var tools []Tool
		if a.config.registry != nil {
			tools = a.config.registry.List()
		} else {
			tools = a.config.tools
		}

		// 构建请求
		chatReq := &ChatRequest{
			Messages:  messages,
			System:    a.config.systemPrompt,
			Tools:     tools,
			MaxTokens: a.config.maxTokens,
		}

		a.trace(ctx, TraceEvent{
			RunID:     runID,
			Type:      "model_request",
			Iteration: iteration,
			Metadata: map[string]any{
				"message_count": len(messages),
				"tool_count":    len(tools),
			},
		})

		// 调用模型
		modelStart := time.Now()
		resp, err := a.requestModel(ctx, chatReq, iteration, req.StreamSink)
		if err != nil {
			wrapped := fmt.Errorf("模型调用失败: %w", err)
			a.trace(ctx, TraceEvent{
				RunID:      runID,
				Type:       "run_error",
				Iteration:  iteration,
				DurationMS: time.Since(modelStart).Milliseconds(),
				Error:      wrapped.Error(),
			})
			return partialResponse(messages, totalInput, totalOutput), wrapped
		}
		a.trace(ctx, TraceEvent{
			RunID:        runID,
			Type:         "model_response",
			Iteration:    iteration,
			StopReason:   resp.StopReason,
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
			DurationMS:   time.Since(modelStart).Milliseconds(),
		})

		totalInput += resp.InputTokens
		totalOutput += resp.OutputTokens

		if containsToolUse(resp.Content) {
			// tool_use 只是模型的调用意图；真正执行发生在这里，结果必须保留原调用 ID。
			var toolResults []ContentBlock
			for _, block := range resp.Content {
				if block.Type() == "tool_use" {
					a.trace(ctx, TraceEvent{
						RunID:     runID,
						Type:      "tool_call",
						Iteration: iteration,
						ToolName:  block.Name(),
						Metadata: map[string]any{
							"input": string(block.Input()),
						},
					})
					toolStart := time.Now()
					result, err := a.executeTool(ctx, block)
					if err != nil {
						result = "工具执行出错: " + err.Error()
					}
					toolEvent := TraceEvent{
						RunID:      runID,
						Type:       "tool_result",
						Iteration:  iteration,
						ToolName:   block.Name(),
						DurationMS: time.Since(toolStart).Milliseconds(),
						Metadata: map[string]any{
							"result_len": len(result),
						},
					}
					if err != nil {
						toolEvent.Error = err.Error()
					}
					a.trace(ctx, toolEvent)
					toolResults = append(toolResults, NewToolResultBlock(block.ID(), result, err != nil))
				}
			}

			// 保持 assistant 的 tool_use 与 user 侧的 tool_result 相邻，压缩时不会留下孤立结果。
			messages = append(messages, Message{
				Role:    "assistant",
				Content: resp.Content,
			})
			messages = append(messages, Message{
				Role:    "user",
				Content: toolResults,
			})
			continue
		}

		// 最终答案（或 max_tokens 截断）
		var text string
		for _, block := range resp.Content {
			if block.Type() == "text" {
				text = block.Text()
				break
			}
		}

		if resp.StopReason == "max_tokens" {
			text += "\n\n⚠️ 警告：模型输出被截断，可能需要增大 MaxTokens"
		}

		messages = append(messages, Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		a.trace(ctx, TraceEvent{
			RunID:        runID,
			Type:         "run_end",
			Iteration:    iteration,
			StopReason:   resp.StopReason,
			InputTokens:  totalInput,
			OutputTokens: totalOutput,
		})

		return &Response{
			Text:         text,
			History:      messages,
			InputTokens:  totalInput,
			OutputTokens: totalOutput,
		}, nil
	}
}

// containsToolUse 只判断模型是否提出工具调用，不执行工具。
func containsToolUse(blocks []ContentBlock) bool {
	for _, block := range blocks {
		if block.Type() == "tool_use" {
			return true
		}
	}
	return false
}

// partialResponse 在 Runtime 中断时保留已经执行过的工具消息，供应用层持久化和恢复。
func partialResponse(messages []Message, inputTokens, outputTokens int) *Response {
	return &Response{
		History:      append([]Message(nil), messages...),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
}

func (a *reactAgent) trace(ctx context.Context, event TraceEvent) {
	if a.config.tracer == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	// Trace 失败不应该让业务请求失败；生产环境可用 stderr tracer 或监控告警处理写入错误。
	_ = a.config.tracer.Record(ctx, event)
}

// executeTool 根据调用名找到工具并执行；未知工具会作为结果返回给模型，而不是让 Run 崩溃。
func (a *reactAgent) executeTool(ctx context.Context, block ContentBlock) (string, error) {
	var tool Tool
	if a.config.registry != nil {
		t, err := a.config.registry.Get(block.Name())
		if err != nil {
			return fmt.Sprintf("工具不存在: %s", block.Name()), nil
		}
		tool = t
	} else {
		for _, t := range a.config.tools {
			if t.Definition().Name == block.Name() {
				tool = t
				break
			}
		}
		if tool == nil {
			return fmt.Sprintf("未知工具: %s", block.Name()), nil
		}
	}

	// The Runtime deliberately has no interactive permission middleware. The
	// execution boundary belongs to the process/container that launches it.
	return tool.Execute(ctx, block.Input())
}
