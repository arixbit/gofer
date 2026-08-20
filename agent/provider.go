package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pkoukk/tiktoken-go"
	openai "github.com/sashabaranov/go-openai"
)

// ModelProvider 抽象模型提供商
type ModelProvider interface {
	// Chat 发送对话请求，返回模型响应
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)

	// CountTokens 计算消息占用的 token 数
	CountTokens(ctx context.Context, messages []Message) (int, error)
}

type ChatRequest struct {
	// Model 是供应商使用的模型名；为空时使用 Provider 默认值。
	Model string
	// Messages 是当前 Runtime 上下文，包含用户、assistant 和工具结果消息。
	Messages []Message
	// System 是本轮请求的系统提示。
	System string
	// Tools 是本轮允许模型选择的工具定义。
	Tools []Tool
	// MaxTokens 限制模型本轮最多生成的 token 数。
	MaxTokens int
	// Temperature 控制采样随机性；由 Provider 决定是否支持。
	Temperature float64
}

type ChatResponse struct {
	// Content 是模型返回的文本或 tool_use 内容块。
	Content []ContentBlock
	// StopReason 表示模型为何停止，例如 tool_use、end_turn 或 max_tokens。
	StopReason string // "tool_use" / "end_turn" / "max_tokens"
	// InputTokens 和 OutputTokens 用于累计本轮成本与上下文预算。
	InputTokens  int
	OutputTokens int
}

// OpenAICompatibleProvider 基于 OpenAI Chat Completions 兼容接口的 ModelProvider 实现。
//
// 它只负责协议转换；具体供应商的地址、模型和鉴权由调用方配置。
// DeepSeekProvider 在此基础上额外强制开启 Thinking Mode。
type OpenAICompatibleProvider struct {
	client       *openai.Client
	defaultModel string
}

// DeepSeekProvider 基于 DeepSeek API 的 ModelProvider 实现。
// 它嵌入 OpenAICompatibleProvider，以保留既有章节的调用方式。
type DeepSeekProvider struct {
	*OpenAICompatibleProvider
}

// deepseekTransport 对 DeepSeek 请求补充协议字段。
//
// 它强制开启 Thinking Mode；reasoning_content 由框架作为 reasoning 块存入历史，
// 在下一轮请求中由 buildChatCompletionRequest 写回 ReasoningContent，满足多轮回传约束。
// 也为被 SDK 因空字符串省略 content 的消息补回该字段。
type deepseekTransport struct {
	base http.RoundTripper
}

func (t *deepseekTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 只处理 DeepSeek API 的 chat completions 请求
	if strings.Contains(req.URL.Host, "deepseek") && strings.Contains(req.URL.Path, "chat/completions") {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err := req.Body.Close(); err != nil {
			return nil, fmt.Errorf("关闭原始请求体失败: %w", err)
		}

		var bodyMap map[string]any
		if err := json.Unmarshal(bodyBytes, &bodyMap); err != nil {
			return nil, fmt.Errorf("请求体解析失败: %w", err)
		}
		ensureMessagesHaveContent(bodyMap)
		if _, exists := bodyMap["thinking"]; !exists {
			bodyMap["thinking"] = map[string]string{"type": "enabled"}
		}
		modifiedBytes, err := json.Marshal(bodyMap)
		if err != nil {
			return nil, err
		}

		req.Body = io.NopCloser(bytes.NewReader(modifiedBytes))
		req.ContentLength = int64(len(modifiedBytes))
	}

	if t.base != nil {
		return t.base.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// ensureMessagesHaveContent adapts the SDK request to DeepSeek’s requirement
// that every message, including an empty tool result, retains a content field.
func ensureMessagesHaveContent(body map[string]any) {
	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		if _, hasContent := message["content"]; !hasContent {
			message["content"] = ""
		}
	}
}

// NewOpenAICompatibleProvider 创建 OpenAI Chat Completions 兼容提供商。
// baseURL 为空时使用 go-openai 的默认地址。
func NewOpenAICompatibleProvider(apiKey, baseURL, defaultModel string) *OpenAICompatibleProvider {
	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = strings.TrimRight(baseURL, "/")
	}
	return newOpenAICompatibleProvider(config, defaultModel)
}

func newOpenAICompatibleProvider(config openai.ClientConfig, defaultModel string) *OpenAICompatibleProvider {
	return &OpenAICompatibleProvider{
		client:       openai.NewClientWithConfig(config),
		defaultModel: defaultModel,
	}
}

// NewDeepSeekProvider 创建默认使用 deepseek-v4-flash 的 DeepSeek 提供商。
func NewDeepSeekProvider(apiKey string) *DeepSeekProvider {
	return NewDeepSeekProviderWithModel(apiKey, "deepseek-v4-flash")
}

// NewDeepSeekProviderWithModel 创建指定默认模型的 DeepSeek 提供商。
// 自动注入 thinking: {type: "enabled"} 以强制开启 V4 Thinking Mode；
// reasoning_content 由框架存入历史并在下一轮回传，避免多轮工具调用 400。
func NewDeepSeekProviderWithModel(apiKey, defaultModel string) *DeepSeekProvider {
	if strings.TrimSpace(defaultModel) == "" {
		defaultModel = "deepseek-v4-flash"
	}
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = "https://api.deepseek.com"

	// 包装 HTTPClient 注入 thinking 开启
	if httpClient, ok := config.HTTPClient.(*http.Client); ok {
		httpClient.Transport = &deepseekTransport{
			base: httpClient.Transport,
		}
	}

	return &DeepSeekProvider{
		OpenAICompatibleProvider: newOpenAICompatibleProvider(config, defaultModel),
	}
}

func (p *OpenAICompatibleProvider) buildChatCompletionRequest(req *ChatRequest) openai.ChatCompletionRequest {
	// 1. 框架 Message → OpenAI ChatCompletionMessage
	var openaiMsgs []openai.ChatCompletionMessage

	if req.System != "" {
		openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: req.System,
		})
	}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "user":
			for _, block := range msg.Content {
				switch block.Type() {
				case "text":
					openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: block.Text(),
					})
				case "tool_result":
					openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    block.Text(),
						ToolCallID: block.ID(),
					})
				}
			}
		case "assistant":
			oaiMsg := openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant,
			}
			for _, block := range msg.Content {
				switch block.Type() {
				case "text":
					oaiMsg.Content = block.Text()
				case "reasoning":
					// 回传上一轮的 reasoning_content，满足 DeepSeek 多轮工具调用约束。
					oaiMsg.ReasoningContent = block.Reasoning()
				case "tool_use":
					oaiMsg.ToolCalls = append(oaiMsg.ToolCalls, openai.ToolCall{
						ID:   block.ID(),
						Type: openai.ToolTypeFunction,
						Function: openai.FunctionCall{
							Name:      block.Name(),
							Arguments: string(block.Input()),
						},
					})
				}
			}
			openaiMsgs = append(openaiMsgs, oaiMsg)
		}
	}

	// 2. 框架 Tool → OpenAI Tool
	var oaiTools []openai.Tool
	for _, t := range req.Tools {
		def := t.Definition()
		oaiTools = append(oaiTools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.InputSchema,
			},
		})
	}

	model := req.Model
	if model == "" {
		model = p.defaultModel
	}

	return openai.ChatCompletionRequest{
		Model:       model,
		Messages:    openaiMsgs,
		Tools:       oaiTools,
		MaxTokens:   req.MaxTokens,
		Temperature: float32(req.Temperature),
	}
}

func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	// deepseekTransport 会自动注入 thinking: enabled。
	resp, err := p.client.CreateChatCompletion(ctx, p.buildChatCompletionRequest(req))
	if err != nil {
		return nil, fmt.Errorf("API 调用失败: %w", err)
	}
	return chatResponseFromOpenAI(resp)
}

// Stream 创建 OpenAI-compatible 的 SSE 流，并在 Runtime 边接收边观察。
func (p *OpenAICompatibleProvider) Stream(ctx context.Context, req *ChatRequest) (ChatStream, error) {
	request := p.buildChatCompletionRequest(req)
	request.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	stream, err := p.client.CreateChatCompletionStream(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("创建流式 API 请求失败: %w", err)
	}
	return &openAIChatStream{stream: stream}, nil
}

func chatResponseFromOpenAI(resp openai.ChatCompletionResponse) (*ChatResponse, error) {

	// 4. OpenAI 响应 → 框架 ChatResponse
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("API 未返回可用的候选结果")
	}
	choice := resp.Choices[0]
	var blocks []ContentBlock

	if choice.Message.ReasoningContent != "" {
		blocks = append(blocks, NewReasoningBlock(choice.Message.ReasoningContent))
	}
	if choice.Message.Content != "" {
		blocks = append(blocks, NewTextBlock(choice.Message.Content))
	}
	for _, tc := range choice.Message.ToolCalls {
		blocks = append(blocks, NewToolUseBlock(
			tc.ID,
			tc.Function.Name,
			json.RawMessage(tc.Function.Arguments),
		))
	}

	stopReason := "end_turn"
	switch choice.FinishReason {
	case openai.FinishReasonToolCalls:
		stopReason = "tool_use"
	case openai.FinishReasonLength:
		stopReason = "max_tokens"
	}

	return &ChatResponse{
		Content:      blocks,
		StopReason:   stopReason,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}, nil
}

type openAIChatStream struct {
	stream  *openai.ChatCompletionStream
	pending []StreamEvent
}

func (s *openAIChatStream) Recv() (StreamEvent, error) {
	for len(s.pending) == 0 {
		chunk, err := s.stream.Recv()
		if err != nil {
			return StreamEvent{}, err
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.ReasoningContent != "" {
				s.pending = append(s.pending, StreamEvent{
					Type:      StreamEventReasoningDelta,
					Reasoning: choice.Delta.ReasoningContent,
				})
			}
			if choice.Delta.Content != "" {
				s.pending = append(s.pending, StreamEvent{
					Type: StreamEventTextDelta,
					Text: choice.Delta.Content,
				})
			}
			for _, toolCall := range choice.Delta.ToolCalls {
				index := 0
				if toolCall.Index != nil {
					index = *toolCall.Index
				}
				s.pending = append(s.pending, StreamEvent{
					Type:           StreamEventToolCallDelta,
					ToolCallIndex:  index,
					ToolCallID:     toolCall.ID,
					ToolName:       toolCall.Function.Name,
					ArgumentsDelta: toolCall.Function.Arguments,
				})
			}
			if choice.FinishReason != "" {
				s.pending = append(s.pending, StreamEvent{
					Type:       StreamEventDone,
					StopReason: openAIStopReason(choice.FinishReason),
				})
			}
		}
		if chunk.Usage != nil {
			s.pending = append(s.pending, StreamEvent{
				Type:         StreamEventUsage,
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			})
		}
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event, nil
}

func (s *openAIChatStream) Close() error {
	return s.stream.Close()
}

func openAIStopReason(reason openai.FinishReason) string {
	switch reason {
	case openai.FinishReasonToolCalls:
		return "tool_use"
	case openai.FinishReasonLength:
		return "max_tokens"
	case openai.FinishReasonStop:
		return "end_turn"
	default:
		return string(reason)
	}
}

func (p *OpenAICompatibleProvider) CountTokens(ctx context.Context, messages []Message) (int, error) {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		// tiktoken 运行时下载 tokenizer 资源，大陆/离线环境可能失败。
		// 回退到字符估算：1 token ≈ 2 字符（保守估计，给压缩留安全边界）
		return fallbackCount(messages), nil
	}
	total := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			tokens := tke.Encode(block.Text(), nil, nil)
			total += len(tokens)
			if block.Type() == "reasoning" {
				total += len(tke.Encode(block.Reasoning(), nil, nil))
			}
		}
		total += 4 // 每条消息的格式开销
	}
	return total, nil
}

// fallbackCount 在 tiktoken 不可用时用字符数估算 token 数。
// 保守估计 1 token ≈ 2 字符，实际英文约 3-4 字符/token，中文约 1.5-1.7 字符/token。
func fallbackCount(messages []Message) int {
	total := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			total += len(block.Text()) / 2
			if block.Type() == "reasoning" {
				total += len(block.Reasoning()) / 2
			}
		}
	}
	return total
}
