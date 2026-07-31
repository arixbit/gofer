package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// TraceEvent 是 Agent 运行过程中的一条可观测事件。
type TraceEvent struct {
	Time         time.Time      `json:"time"`
	RunID        string         `json:"run_id"`
	Type         string         `json:"type"`
	Iteration    int            `json:"iteration,omitempty"`
	Message      string         `json:"message,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	StopReason   string         `json:"stop_reason,omitempty"`
	InputTokens  int            `json:"input_tokens,omitempty"`
	OutputTokens int            `json:"output_tokens,omitempty"`
	DurationMS   int64          `json:"duration_ms,omitempty"`
	Error        string         `json:"error,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Tracer 记录 Agent 运行轨迹。
type Tracer interface {
	Record(ctx context.Context, event TraceEvent) error
}

// NoopTracer 丢弃所有 TraceEvent。
type NoopTracer struct{}

// Record 实现 Tracer 接口。
func (NoopTracer) Record(ctx context.Context, event TraceEvent) error {
	return nil
}

// JSONLTracer 将 TraceEvent 逐行写成 JSONL。
type JSONLTracer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONLTracer 创建写入指定 writer 的 JSONL tracer。
func NewJSONLTracer(w io.Writer) *JSONLTracer {
	return &JSONLTracer{w: w}
}

// NewJSONLFileTracer 创建写入文件的 JSONL tracer，并返回关闭函数。
func NewJSONLFileTracer(path string) (*JSONLTracer, func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 trace 文件失败: %w", err)
	}
	return NewJSONLTracer(f), f.Close, nil
}

// Record 写入一行 JSON 事件。
func (t *JSONLTracer) Record(ctx context.Context, event TraceEvent) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.w == nil {
		return fmt.Errorf("trace writer 未配置")
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("序列化 trace 事件失败: %w", err)
	}
	if _, err := t.w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("写入 trace 事件失败: %w", err)
	}
	return nil
}

// ConsoleTracer 用简短的运行状态展示 Agent 的执行过程。
// 它刻意忽略用户消息、工具参数和工具结果，避免把对话内容重复输出到终端。
type ConsoleTracer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewConsoleTracer 创建写入指定 writer 的控制台 tracer。
func NewConsoleTracer(w io.Writer) *ConsoleTracer {
	return &ConsoleTracer{w: w}
}

// Record 将可安全展示的生命周期事件格式化为一行终端输出。
func (t *ConsoleTracer) Record(ctx context.Context, event TraceEvent) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.w == nil {
		return fmt.Errorf("trace writer 未配置")
	}
	line, ok := consoleTraceLine(event)
	if !ok {
		return nil
	}
	if _, err := fmt.Fprintln(t.w, line); err != nil {
		return fmt.Errorf("写入控制台 trace 失败: %w", err)
	}
	return nil
}

func consoleTraceLine(event TraceEvent) (string, bool) {
	switch event.Type {
	case "run_start":
		return "[Runtime] 开始处理本次请求", true
	case "model_request":
		return fmt.Sprintf(
			"[Runtime] 第 %d 轮：请求模型（历史 %d 条，可用工具 %d 个）",
			event.Iteration,
			metadataInt(event.Metadata, "message_count"),
			metadataInt(event.Metadata, "tool_count"),
		), true
	case "model_response":
		return fmt.Sprintf(
			"[Runtime] 第 %d 轮：%s（%dms）",
			event.Iteration,
			modelResponseSummary(event.StopReason),
			event.DurationMS,
		), true
	case "tool_call":
		return fmt.Sprintf("[Runtime] 第 %d 轮：调用工具 %s", event.Iteration, event.ToolName), true
	case "tool_result":
		if event.Error != "" {
			return fmt.Sprintf(
				"[Runtime] 第 %d 轮：工具 %s 失败（%dms）",
				event.Iteration,
				event.ToolName,
				event.DurationMS,
			), true
		}
		return fmt.Sprintf(
			"[Runtime] 第 %d 轮：工具 %s 完成（%dms）",
			event.Iteration,
			event.ToolName,
			event.DurationMS,
		), true
	case "run_end":
		return fmt.Sprintf(
			"[Runtime] 第 %d 轮：完成（输入 %d tokens，输出 %d tokens）",
			event.Iteration,
			event.InputTokens,
			event.OutputTokens,
		), true
	case "run_error":
		return fmt.Sprintf("[Runtime] 第 %d 轮：执行失败", event.Iteration), true
	default:
		return "", false
	}
}

func modelResponseSummary(stopReason string) string {
	switch stopReason {
	case "tool_use":
		return "模型请求工具"
	case "end_turn":
		return "模型给出最终回答"
	default:
		return fmt.Sprintf("模型响应（停止原因：%s）", stopReason)
	}
}

func metadataInt(metadata map[string]any, key string) int {
	if value, ok := metadata[key].(int); ok {
		return value
	}
	return 0
}
