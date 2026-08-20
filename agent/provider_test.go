package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestOpenAICompatibleProviderUsesConfiguredBaseURLAndModel(t *testing.T) {
	config := openai.DefaultConfig("test-key")
	config.BaseURL = "https://example.test/v1"
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request path = %q, want /v1/chat/completions", request.URL.Path)
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "example-model" {
			t.Fatalf("request model = %q, want example-model", body.Model)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
  "id":"chatcmpl_test",
  "object":"chat.completion",
  "created":0,
  "model":"example-model",
  "choices":[{"index":0,"message":{"role":"assistant","content":"完成"},"finish_reason":"stop"}],
  "usage":{"prompt_tokens":3,"completion_tokens":2}
}`)),
		}, nil
	})}

	provider := newOpenAICompatibleProvider(config, "example-model")
	response, err := provider.Chat(context.Background(), &ChatRequest{
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{NewTextBlock("你好")},
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if response.StopReason != "end_turn" || len(response.Content) != 1 || response.Content[0].Text() != "完成" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestDeepSeekProviderWithModelKeepsConfiguredDefault(t *testing.T) {
	provider := NewDeepSeekProviderWithModel("test-key", "deepseek-custom")
	if provider.defaultModel != "deepseek-custom" {
		t.Fatalf("default model = %q, want deepseek-custom", provider.defaultModel)
	}
}

func TestEnsureMessagesHaveContent(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role":       "assistant",
				"tool_calls": []any{map[string]any{"id": "call_1"}},
			},
			map[string]any{
				"role":    "assistant",
				"content": "已有文本",
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
			},
		},
	}

	ensureMessagesHaveContent(body)
	messages := body["messages"].([]any)
	toolCallMessage := messages[0].(map[string]any)
	if content, ok := toolCallMessage["content"]; !ok || content != "" {
		t.Fatalf("tool-call message content = %#v, present = %v; want empty string field", content, ok)
	}
	if _, exists := messages[1].(map[string]any)["content"]; !exists {
		t.Fatal("assistant text message lost content")
	}
	toolResultMessage := messages[2].(map[string]any)
	if content, ok := toolResultMessage["content"]; !ok || content != "" {
		t.Fatalf("tool-result message content = %#v, present = %v; want empty string field", content, ok)
	}
}

func TestDeepSeekTransportWritesContentForToolCallMessage(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://api.deepseek.com/chat/completions", strings.NewReader(`{
  "messages":[
    {"role":"assistant","tool_calls":[{"id":"call_1"}]},
    {"role":"tool","tool_call_id":"call_1"}
  ]
}`))
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}

	transport := deepseekTransport{base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Messages []map[string]json.RawMessage `json:"messages"`
			Thinking map[string]string            `json:"thinking"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Messages) != 2 {
			t.Fatalf("messages length = %d, want 2", len(body.Messages))
		}
		for i, message := range body.Messages {
			content, ok := message["content"]
			if !ok || string(content) != `""` {
				t.Fatalf("message %d content = %s, present = %v; want empty JSON string", i, content, ok)
			}
		}
		if body.Thinking["type"] != "enabled" {
			t.Fatalf("thinking type = %q, want \"enabled\"", body.Thinking["type"])
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}

	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip() error: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
}

func TestDeepSeekProviderWritesContentForEmptyToolResult(t *testing.T) {
	config := openai.DefaultConfig("test-key")
	config.BaseURL = "https://api.deepseek.com"
	config.HTTPClient = &http.Client{Transport: &deepseekTransport{base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Messages) != 2 {
			t.Fatalf("messages length = %d, want 2", len(body.Messages))
		}
		for i, message := range body.Messages {
			content, ok := message["content"]
			if !ok || string(content) != `""` {
				t.Fatalf("message %d content = %s, present = %v; want empty JSON string", i, content, ok)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
  "choices":[{"message":{"role":"assistant","content":"完成"},"finish_reason":"stop"}]
}`)),
		}, nil
	})}}
	provider := &DeepSeekProvider{OpenAICompatibleProvider: newOpenAICompatibleProvider(config, "deepseek-test")}

	_, err := provider.Chat(context.Background(), &ChatRequest{Messages: []Message{
		{Role: "assistant", Content: []ContentBlock{
			NewToolUseBlock("call_1", "bash", json.RawMessage(`{"command":"rm hello.go"}`)),
		}},
		{Role: "user", Content: []ContentBlock{NewToolResultBlock("call_1", "", false)}},
	}})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
}

// TestReasoningRoundTrip 验证思考内容的完整闭环：
// 第一轮模型返回 reasoning_content，框架应捕获成 reasoning 块并存入历史；
// 第二轮带上该历史发起请求时，reasoning 必须被写回 ReasoningContent，满足 DeepSeek 多轮约束。
func TestReasoningRoundTrip(t *testing.T) {
	var firstRequest, secondRequest struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"messages"`
	}
	callCount := 0

	config := openai.DefaultConfig("test-key")
	config.BaseURL = "https://api.deepseek.com"
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		callCount++
		var target *struct {
			Messages []struct {
				Role             string `json:"role"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"messages"`
		}
		if callCount == 1 {
			target = &firstRequest
		} else {
			target = &secondRequest
		}
		if err := json.NewDecoder(request.Body).Decode(target); err != nil {
			t.Fatalf("decode request %d: %v", callCount, err)
		}

		// 第一轮返回 reasoning + tool_use；第二轮返回纯文本。
		var respBody string
		if callCount == 1 {
			respBody = `{"choices":[{"message":{"role":"assistant","reasoning_content":"我先想想","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
		} else {
			respBody = `{"choices":[{"message":{"role":"assistant","content":"好了"},"finish_reason":"stop"}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(respBody)),
		}, nil
	})}

	provider := newOpenAICompatibleProvider(config, "deepseek-test")

	// 第一轮：模型给出 reasoning + tool_use，框架执行伪工具后保留历史。
	firstResp, err := provider.Chat(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{NewTextBlock("run")}}},
		Tools:    stubTools("bash"),
	})
	if err != nil {
		t.Fatalf("first Chat() error: %v", err)
	}
	var sawReasoning bool
	for _, block := range firstResp.Content {
		if block.Type() == "reasoning" {
			sawReasoning = true
			if block.Reasoning() != "我先想想" {
				t.Fatalf("reasoning = %q, want %q", block.Reasoning(), "我先想想")
			}
		}
	}
	if !sawReasoning {
		t.Fatal("第一轮响应未捕获 reasoning 块")
	}

	// 把 assistant 响应原样存入历史，再补上伪 tool_result，模拟 Runtime 的多轮拼接。
	history := []Message{
		{Role: "user", Content: []ContentBlock{NewTextBlock("run")}},
		{Role: "assistant", Content: firstResp.Content},
		{Role: "user", Content: []ContentBlock{NewToolResultBlock("call_1", "done", false)}},
	}

	// 第二轮：带历史发请求，验证 reasoning 被写回 ReasoningContent。
	if _, err := provider.Chat(context.Background(), &ChatRequest{
		Messages: history,
		Tools:    stubTools("bash"),
	}); err != nil {
		t.Fatalf("second Chat() error: %v", err)
	}

	// 第二轮请求里的 assistant 消息（history[1]）必须带 reasoning_content。
	var assistantReasoning string
	for _, msg := range secondRequest.Messages {
		if msg.Role == "assistant" && msg.ReasoningContent != "" {
			assistantReasoning = msg.ReasoningContent
		}
	}
	if assistantReasoning != "我先想想" {
		t.Fatalf("第二轮请求未回传 reasoning_content，got %q", assistantReasoning)
	}
}

func stubTools(names ...string) []Tool {
	tools := make([]Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, &stubTool{name: name})
	}
	return tools
}

type stubTool struct{ name string }

func (t *stubTool) Definition() ToolDefinition {
	return ToolDefinition{Name: t.name, Description: "stub", InputSchema: map[string]any{"type": "object"}}
}
func (t *stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", nil
}
