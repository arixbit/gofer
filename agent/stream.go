package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// StreamEventType 是模型流式响应中的事件类型。
type StreamEventType string

const (
	// StreamEventStart 表示一轮模型流开始。
	StreamEventStart StreamEventType = "start"
	// StreamEventTextDelta 表示新增的一小段文本。
	StreamEventTextDelta StreamEventType = "text_delta"
	// StreamEventToolCallDelta 表示工具调用的 ID、名称或参数片段。
	StreamEventToolCallDelta StreamEventType = "tool_call_delta"
	// StreamEventDone 表示模型已经给出停止原因。
	StreamEventDone StreamEventType = "done"
	// StreamEventUsage 表示供应商在流末尾补充的 token 统计。
	StreamEventUsage StreamEventType = "usage"
)

// StreamEvent 是 Provider 与 Runtime 之间的规范化流事件。
// TextDelta 和 ArgumentsDelta 可以逐个事件到达，不能把单个事件当成完整消息。
type StreamEvent struct {
	Type           StreamEventType
	Iteration      int
	Text           string
	ToolCallIndex  int
	ToolCallID     string
	ToolName       string
	ArgumentsDelta string
	StopReason     string
	InputTokens    int
	OutputTokens   int
}

// StreamSink 接收模型流事件。返回错误会中止当前模型流。
type StreamSink func(context.Context, StreamEvent) error

// ChatStream 是一次可取消的模型响应流。
// Stream 创建时绑定 context；Close 必须释放底层 HTTP 连接。
type ChatStream interface {
	Recv() (StreamEvent, error)
	Close() error
}

// StreamingModelProvider 在保留同步 Chat 的同时提供流式请求能力。
// Runtime 会优先使用 Stream；不支持流式的 Provider 仍可走 Chat 回退路径。
type StreamingModelProvider interface {
	ModelProvider
	Stream(context.Context, *ChatRequest) (ChatStream, error)
}

// ErrStreamingUnsupported 表示 Provider 没有实现流式请求。
var ErrStreamingUnsupported = errors.New("provider 不支持流式请求")

type assembledToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

func (a *reactAgent) requestModel(ctx context.Context, request *ChatRequest, iteration int, sink StreamSink) (*ChatResponse, error) {
	provider, ok := a.provider.(StreamingModelProvider)
	if !ok {
		return a.provider.Chat(ctx, request)
	}
	stream, err := provider.Stream(ctx, request)
	if errors.Is(err, ErrStreamingUnsupported) {
		return a.provider.Chat(ctx, request)
	}
	if err != nil {
		return nil, err
	}
	if sink != nil {
		if err := sink(ctx, StreamEvent{Type: StreamEventStart, Iteration: iteration}); err != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("开始模型流失败: %w", err)
		}
	}
	response, consumeErr := consumeChatStream(ctx, stream, iteration, sink)
	closeErr := stream.Close()
	if consumeErr != nil {
		return nil, consumeErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("关闭模型流失败: %w", closeErr)
	}
	return response, nil
}

// consumeChatStream 把增量事件组装成 Runtime 可以处理的 ChatResponse。
// 工具调用必须等参数流结束后才构造成 tool_use，避免执行半截 JSON。
func consumeChatStream(ctx context.Context, stream ChatStream, iteration int, sink StreamSink) (*ChatResponse, error) {
	if stream == nil {
		return nil, fmt.Errorf("Provider 返回了 nil 流")
	}

	var text strings.Builder
	toolCalls := make(map[int]*assembledToolCall)
	var toolOrder []int
	stopReason := ""
	sawDone := false
	inputTokens, outputTokens := 0, 0

	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取模型流失败: %w", err)
		}
		event.Iteration = iteration
		if sink != nil {
			if err := sink(ctx, event); err != nil {
				return nil, fmt.Errorf("处理模型流事件失败: %w", err)
			}
		}

		switch event.Type {
		case StreamEventTextDelta:
			text.WriteString(event.Text)
		case StreamEventToolCallDelta:
			call, ok := toolCalls[event.ToolCallIndex]
			if !ok {
				call = &assembledToolCall{}
				toolCalls[event.ToolCallIndex] = call
				toolOrder = append(toolOrder, event.ToolCallIndex)
			}
			if event.ToolCallID != "" {
				call.id = event.ToolCallID
			}
			if event.ToolName != "" {
				call.name = event.ToolName
			}
			call.arguments.WriteString(event.ArgumentsDelta)
		case StreamEventDone:
			stopReason = event.StopReason
			sawDone = true
		case StreamEventUsage:
			inputTokens = event.InputTokens
			outputTokens = event.OutputTokens
		}
	}

	blocks := make([]ContentBlock, 0, 1+len(toolOrder))
	if text.Len() > 0 {
		blocks = append(blocks, NewTextBlock(text.String()))
	}
	for _, index := range toolOrder {
		call := toolCalls[index]
		if call.id == "" || call.name == "" {
			return nil, fmt.Errorf("流式工具调用 %d 缺少 id 或 name", index)
		}
		arguments := strings.TrimSpace(call.arguments.String())
		if arguments == "" {
			arguments = "{}"
		}
		blocks = append(blocks, NewToolUseBlock(call.id, call.name, []byte(arguments)))
	}

	if stopReason == "" {
		if len(toolOrder) > 0 {
			stopReason = "tool_use"
		} else {
			stopReason = "end_turn"
		}
	}
	if !sawDone && sink != nil {
		if err := sink(ctx, StreamEvent{
			Type:         StreamEventDone,
			Iteration:    iteration,
			StopReason:   stopReason,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		}); err != nil {
			return nil, fmt.Errorf("结束模型流失败: %w", err)
		}
	}
	return &ChatResponse{
		Content:      blocks,
		StopReason:   stopReason,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, nil
}
