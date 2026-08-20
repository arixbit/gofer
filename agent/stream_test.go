package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

type fakeChatStream struct {
	events []StreamEvent
	index  int
	closed bool
}

func (s *fakeChatStream) Recv() (StreamEvent, error) {
	if s.index >= len(s.events) {
		return StreamEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *fakeChatStream) Close() error {
	s.closed = true
	return nil
}

type fakeStreamingProvider struct {
	streams [][]StreamEvent
	opened  []*fakeChatStream
	calls   int
}

func (p *fakeStreamingProvider) Chat(context.Context, *ChatRequest) (*ChatResponse, error) {
	return nil, fmt.Errorf("测试不应回退到 Chat")
}

func (p *fakeStreamingProvider) CountTokens(context.Context, []Message) (int, error) {
	return 10, nil
}

func (p *fakeStreamingProvider) Stream(context.Context, *ChatRequest) (ChatStream, error) {
	if p.calls >= len(p.streams) {
		return nil, fmt.Errorf("没有第 %d 次脚本流", p.calls+1)
	}
	stream := &fakeChatStream{events: p.streams[p.calls]}
	p.calls++
	p.opened = append(p.opened, stream)
	return stream, nil
}

func TestAgentRunConsumesStreamingResponseAndEmitsEvents(t *testing.T) {
	provider := &fakeStreamingProvider{streams: [][]StreamEvent{
		{
			{Type: StreamEventTextDelta, Text: "先读取文件。"},
			{Type: StreamEventToolCallDelta, ToolCallIndex: 0, ToolCallID: "call_1", ToolName: "echo", ArgumentsDelta: `{"text":"he`},
			{Type: StreamEventToolCallDelta, ToolCallIndex: 0, ArgumentsDelta: `llo"}`},
			{Type: StreamEventDone, StopReason: "tool_use"},
			{Type: StreamEventUsage, InputTokens: 3, OutputTokens: 4},
		},
		{
			{Type: StreamEventTextDelta, Text: "完成。"},
			{Type: StreamEventDone, StopReason: "end_turn"},
			{Type: StreamEventUsage, InputTokens: 5, OutputTokens: 2},
		},
	}}
	registry := NewToolRegistry()
	if err := registry.Register(&echoTool{}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	var events []StreamEvent
	runtime := NewAgent(provider, WithToolRegistry(registry))
	response, err := runtime.Run(context.Background(), Request{
		Message: "读取文件",
		StreamSink: func(_ context.Context, event StreamEvent) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if response.Text != "完成。" {
		t.Fatalf("response.Text = %q, want 完成。", response.Text)
	}
	if provider.calls != 2 {
		t.Fatalf("stream calls = %d, want 2", provider.calls)
	}
	for i, stream := range provider.opened {
		if !stream.closed {
			t.Errorf("stream %d was not closed", i+1)
		}
	}
	if !containsToolResult(response.History, "call_1", "hello") {
		t.Fatalf("history lost streamed tool result: %#v", response.History)
	}
	if response.InputTokens != 8 || response.OutputTokens != 6 {
		t.Fatalf("token totals = %d/%d, want 8/6", response.InputTokens, response.OutputTokens)
	}
	if countStreamEvents(events, StreamEventStart) != 2 || countStreamEvents(events, StreamEventDone) != 2 {
		t.Fatalf("events = %#v, want two start and done events", events)
	}
	if countStreamEvents(events, StreamEventTextDelta) != 2 || countStreamEvents(events, StreamEventToolCallDelta) != 2 {
		t.Fatalf("events = %#v, want text and tool-call deltas", events)
	}
	for _, event := range events {
		if event.Iteration < 1 {
			t.Fatalf("event iteration = %d, want positive iteration: %#v", event.Iteration, event)
		}
	}
}

func TestConsumeChatStreamRejectsIncompleteToolCall(t *testing.T) {
	stream := &fakeChatStream{events: []StreamEvent{
		{Type: StreamEventToolCallDelta, ToolCallIndex: 0, ToolName: "echo", ArgumentsDelta: `{}`},
		{Type: StreamEventDone, StopReason: "tool_use"},
	}}
	_, err := consumeChatStream(context.Background(), stream, 1, nil)
	if err == nil {
		t.Fatal("consumeChatStream() error = nil, want incomplete tool call error")
	}
	if !stream.closed {
		// consumeChatStream does not own the stream lifetime; the Runtime does.
		t.Log("stream remains open because its caller owns Close")
	}
}

func countStreamEvents(events []StreamEvent, eventType StreamEventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func TestOpenAICompatibleProviderStreamNormalizesSSE(t *testing.T) {
	config := testOpenAIConfig(`data: {"id":"chatcmpl_stream","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"}}]}

data: {"id":"chatcmpl_stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"path\":\"he"}}]}}]}

data: {"id":"chatcmpl_stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"llo.go\"}"}}]}}]}

data: {"id":"chatcmpl_stream","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl_stream","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":5}}

data: [DONE]

`)
	provider := newOpenAICompatibleProvider(config, "stream-model")
	stream, err := provider.Stream(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: []ContentBlock{NewTextBlock("读取")}}}})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	defer stream.Close()
	var events []StreamEvent
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() error: %v", recvErr)
		}
		events = append(events, event)
	}
	if countStreamEvents(events, StreamEventTextDelta) != 1 || events[0].Text != "你好" {
		t.Fatalf("text events = %#v", events)
	}
	if countStreamEvents(events, StreamEventToolCallDelta) != 2 {
		t.Fatalf("tool events = %#v, want two deltas", events)
	}
	if countStreamEvents(events, StreamEventDone) != 1 || countStreamEvents(events, StreamEventUsage) != 1 {
		t.Fatalf("terminal events = %#v", events)
	}

	response, err := consumeChatStream(context.Background(), &eventSliceStream{events: events}, 1, nil)
	if err != nil {
		t.Fatalf("consumeChatStream() error: %v", err)
	}
	if len(response.Content) != 2 || response.Content[0].Text() != "你好" || response.Content[1].ID() != "call_1" {
		t.Fatalf("assembled response = %#v", response)
	}
	if string(response.Content[1].Input()) != `{"path":"hello.go"}` {
		t.Fatalf("assembled tool input = %s", response.Content[1].Input())
	}
	if response.InputTokens != 7 || response.OutputTokens != 5 {
		t.Fatalf("usage = %d/%d, want 7/5", response.InputTokens, response.OutputTokens)
	}
}

type eventSliceStream struct {
	events []StreamEvent
	index  int
}

func (s *eventSliceStream) Recv() (StreamEvent, error) {
	if s.index >= len(s.events) {
		return StreamEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (*eventSliceStream) Close() error { return nil }

func TestOpenAIChatStreamEmitsReasoningDelta(t *testing.T) {
	config := testOpenAIConfig(`data: {"id":"chatcmpl_rs","choices":[{"index":0,"delta":{"reasoning_content":"先"}}]}

data: {"id":"chatcmpl_rs","choices":[{"index":0,"delta":{"reasoning_content":"想想"}}]}

data: {"id":"chatcmpl_rs","choices":[{"index":0,"delta":{"content":"好了"},"finish_reason":"stop"}]}

data: [DONE]

`)
	provider := newOpenAICompatibleProvider(config, "stream-model")
	stream, err := provider.Stream(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: []ContentBlock{NewTextBlock("hi")}}}})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	defer stream.Close()

	var events []StreamEvent
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() error: %v", recvErr)
		}
		events = append(events, event)
	}
	if countStreamEvents(events, StreamEventReasoningDelta) != 2 {
		t.Fatalf("reasoning events = %#v, want 2", events)
	}
	if events[0].Reasoning != "先" || events[1].Reasoning != "想想" {
		t.Fatalf("reasoning deltas = %q / %q, want 先 / 想想", events[0].Reasoning, events[1].Reasoning)
	}
}

func TestConsumeChatStreamAssemblesReasoningBeforeText(t *testing.T) {
	stream := &fakeChatStream{events: []StreamEvent{
		{Type: StreamEventReasoningDelta, Reasoning: "我先"},
		{Type: StreamEventReasoningDelta, Reasoning: "想想"},
		{Type: StreamEventTextDelta, Text: "好了"},
		{Type: StreamEventDone, StopReason: "end_turn"},
		{Type: StreamEventUsage, InputTokens: 3, OutputTokens: 5},
	}}
	response, err := consumeChatStream(context.Background(), stream, 1, nil)
	if err != nil {
		t.Fatalf("consumeChatStream() error: %v", err)
	}
	if len(response.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (reasoning + text)", len(response.Content))
	}
	if response.Content[0].Type() != "reasoning" || response.Content[0].Reasoning() != "我先想想" {
		t.Fatalf("reasoning block = %#v, want reasoning 我先想想", response.Content[0])
	}
	if response.Content[1].Type() != "text" || response.Content[1].Text() != "好了" {
		t.Fatalf("text block = %#v, want text 好了", response.Content[1])
	}
	if response.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", response.StopReason)
	}
	if response.InputTokens != 3 || response.OutputTokens != 5 {
		t.Fatalf("usage = %d/%d, want 3/5", response.InputTokens, response.OutputTokens)
	}
}

func testOpenAIConfig(streamBody string) openai.ClientConfig {
	config := openai.DefaultConfig("test-key")
	config.BaseURL = "https://example.test/v1"
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(streamBody)),
		}, nil
	})}
	return config
}
