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
