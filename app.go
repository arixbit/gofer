package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/arixbit/gofer/agent"
)

type Application struct {
	runtime      agent.Agent
	registry     *agent.ToolRegistry
	session      SessionStore
	history      []agent.Message
	transcript   []agent.Message
	config       Config
	commands     *CommandRegistry
	streamOutput io.Writer
}

const (
	defaultModelRequestTimeout = 90 * time.Second
	// 单个用户回合也有总时限，避免模型连续提出工具调用而长期占用 REPL。
	defaultAgentRunTimeout = 10 * time.Minute
)

func buildApplication(ctx context.Context, config Config, factory providerFactory) (*Application, io.Closer, error) {
	return buildApplicationWithConnectorAndTracer(
		ctx,
		config,
		factory,
		connectMCPTools,
		agent.NewConsoleTracer(os.Stderr),
	)
}

type mcpConnector func(context.Context, []MCPServerConfig) ([]agent.Tool, io.Closer, error)

func buildApplicationWithConnector(ctx context.Context, config Config, factory providerFactory, connector mcpConnector) (*Application, io.Closer, error) {
	return buildApplicationWithConnectorAndTracer(ctx, config, factory, connector, agent.NoopTracer{})
}

func buildApplicationWithConnectorAndTracer(
	ctx context.Context,
	config Config,
	factory providerFactory,
	connector mcpConnector,
	tracer agent.Tracer,
) (*Application, io.Closer, error) {
	workspace, err := NewWorkspace(config.Workspace)
	if err != nil {
		return nil, nil, err
	}
	prompt, err := buildSystemPrompt(workspace.Root(), config.SystemPrompt)
	if err != nil {
		return nil, nil, err
	}
	provider, err := factory(config.Provider)
	if err != nil {
		return nil, nil, err
	}

	registry := agent.NewToolRegistry()
	application := &Application{
		registry: registry,
		session:  NewFileSessionStore(config.SessionDir, workspace.Root()),
		config:   config,
		commands: NewCommandRegistry(),
	}
	for _, tool := range []agent.Tool{
		NewReadFileTool(workspace),
		NewBashTool(workspace),
		NewEditFileTool(workspace),
		NewWriteFileTool(workspace),
	} {
		if err := application.RegisterTool(tool); err != nil {
			return nil, nil, err
		}
	}

	if len(config.Skills) > 0 {
		loader, err := newSkillLoader(config.Skills)
		if err != nil {
			return nil, nil, err
		}
		if err := application.RegisterTool(loader); err != nil {
			return nil, nil, err
		}
	}

	mcpTools, closer, err := connector(ctx, config.MCPServers)
	if err != nil {
		return nil, nil, err
	}
	for _, tool := range mcpTools {
		if err := application.RegisterTool(tool); err != nil {
			return nil, nil, closeMCPOnError(closer, err)
		}
	}

	application.runtime = agent.NewAgent(
		newModelTimeoutProvider(provider, defaultModelRequestTimeout),
		agent.WithToolRegistry(registry),
		agent.WithSystemPrompt(prompt),
		agent.WithTracer(tracer),
	)
	if err := application.installDefaultCommands(); err != nil {
		return nil, nil, closeMCPOnError(closer, err)
	}
	// 每次启动都从空上下文开始；只有 /resume 才会装入旧会话。
	return application, closer, nil
}

func closeMCPOnError(closer io.Closer, cause error) error {
	if closeErr := closer.Close(); closeErr != nil {
		return fmt.Errorf("%w; 关闭 MCP 连接: %v", cause, closeErr)
	}
	return cause
}

// RegisterTool 让应用层可以在启动时注册自己的 agent.Tool。
// Runtime 持有同一个 registry，因此后续注册也会出现在下一轮模型请求里。
func (a *Application) RegisterTool(tool agent.Tool) error {
	return a.registry.Register(tool)
}

// RegisterCommand 注册一个只由本地 REPL 处理的命令，不会进入模型上下文。
func (a *Application) RegisterCommand(command Command) error {
	return a.commands.Register(command)
}

// SetStreamOutput 配置模型文本增量的输出位置；为空时只返回最终文本。
func (a *Application) SetStreamOutput(output io.Writer) {
	a.streamOutput = output
}

func (a *Application) Run(ctx context.Context, input string) (string, error) {
	runContext, cancel := context.WithTimeout(ctx, defaultAgentRunTimeout)
	defer cancel()

	streamedAnswer := false
	lineOpen := false
	var streamSink agent.StreamSink
	if a.streamOutput != nil {
		streamSink = func(_ context.Context, event agent.StreamEvent) error {
			switch event.Type {
			case agent.StreamEventTextDelta:
				if event.Text == "" {
					return nil
				}
				streamedAnswer = true
				lineOpen = true
				_, err := fmt.Fprint(a.streamOutput, event.Text)
				return err
			case agent.StreamEventDone:
				if !lineOpen {
					return nil
				}
				lineOpen = false
				_, err := fmt.Fprintln(a.streamOutput)
				return err
			}
			return nil
		}
	}
	response, runErr := a.runtime.Run(runContext, agent.Request{
		Message:    input,
		History:    a.history,
		StreamSink: streamSink,
	})
	if runErr != nil && lineOpen && a.streamOutput != nil {
		_, _ = fmt.Fprintln(a.streamOutput)
	}
	if response == nil {
		return "", runErr
	}
	if err := a.persistResponse(input, response); err != nil {
		return "", err
	}
	if runErr != nil {
		return "", runErr
	}
	if streamedAnswer {
		return "", nil
	}
	return response.Text, nil
}

func (a *Application) persistResponse(input string, response *agent.Response) error {
	turn, err := currentTurn(response.History, input)
	if err != nil {
		return err
	}
	transcript := append([]agent.Message(nil), a.transcript...)
	transcript = append(transcript, turn...)
	state := SessionState{
		Context:    response.History,
		Transcript: transcript,
	}
	if err := a.session.Save(state); err != nil {
		return fmt.Errorf("保存会话: %w", err)
	}
	a.history = state.Context
	a.transcript = state.Transcript
	return nil
}

func currentTurn(messages []agent.Message, input string) ([]agent.Message, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if block.Type() == "text" && block.Text() == input {
				return messages[i:], nil
			}
		}
	}
	return nil, fmt.Errorf("无法从 Runtime 响应中定位当前用户消息")
}

type modelTimeoutProvider struct {
	provider agent.ModelProvider
	timeout  time.Duration
}

func newModelTimeoutProvider(provider agent.ModelProvider, timeout time.Duration) *modelTimeoutProvider {
	return &modelTimeoutProvider{provider: provider, timeout: timeout}
}

func (p *modelTimeoutProvider) Chat(ctx context.Context, request *agent.ChatRequest) (*agent.ChatResponse, error) {
	requestContext, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	return p.provider.Chat(requestContext, request)
}

func (p *modelTimeoutProvider) Stream(ctx context.Context, request *agent.ChatRequest) (agent.ChatStream, error) {
	provider, ok := p.provider.(agent.StreamingModelProvider)
	if !ok {
		return nil, agent.ErrStreamingUnsupported
	}
	requestContext, cancel := context.WithTimeout(ctx, p.timeout)
	stream, err := provider.Stream(requestContext, request)
	if err != nil {
		cancel()
		return nil, err
	}
	return &modelTimeoutStream{ChatStream: stream, cancel: cancel}, nil
}

type modelTimeoutStream struct {
	agent.ChatStream
	cancel context.CancelFunc
}

func (s *modelTimeoutStream) Close() error {
	err := s.ChatStream.Close()
	s.cancel()
	return err
}

func (p *modelTimeoutProvider) CountTokens(ctx context.Context, messages []agent.Message) (int, error) {
	return p.provider.CountTokens(ctx, messages)
}

func (a *Application) HandleLine(ctx context.Context, line string) (output string, exit bool, handled bool, err error) {
	handled, result, err := a.commands.TryRun(ctx, line)
	if err != nil || handled {
		return result.Output, result.Exit, handled, err
	}
	output, err = a.Run(ctx, line)
	return output, false, false, err
}

func (a *Application) installDefaultCommands() error {
	commands := []Command{
		{
			Name: "/help",
			Help: "查看本地命令；这些命令不会发送给模型。",
			Run: func(context.Context, []string) (CommandResult, error) {
				return CommandResult{Output: a.commands.Help()}, nil
			},
		},
		{
			Name: "/new",
			Help: "创建一个新会话，不覆盖旧的会话记录。",
			Run: func(context.Context, []string) (CommandResult, error) {
				if catalog, ok := a.session.(SessionCatalog); ok {
					if _, err := catalog.NewSession(); err != nil {
						return CommandResult{}, fmt.Errorf("创建新会话: %w", err)
					}
					a.history = nil
					a.transcript = nil
					return CommandResult{Output: "已创建新会话，旧记录仍然保留。"}, nil
				}
				if err := a.session.Save(SessionState{}); err != nil {
					return CommandResult{}, fmt.Errorf("清空会话: %w", err)
				}
				a.history = nil
				a.transcript = nil
				return CommandResult{Output: "已开始新会话。"}, nil
			},
		},
		{
			Name: "/history",
			Help: "列出已经保存的会话。",
			Run: func(context.Context, []string) (CommandResult, error) {
				if catalog, ok := a.session.(SessionCatalog); ok {
					sessions, err := catalog.ListSessions()
					if err != nil {
						return CommandResult{}, fmt.Errorf("列出会话: %w", err)
					}
					if len(sessions) == 0 {
						return CommandResult{Output: "还没有保存的会话。"}, nil
					}
					lines := make([]string, 0, len(sessions))
					for _, session := range sessions {
						marker := "  "
						if session.Current {
							marker = "* "
						}
						lines = append(lines, fmt.Sprintf("%s%s  %s", marker, session.ID, session.CreatedAt.Local().Format("2006-01-02 15:04")))
					}
					return CommandResult{Output: "已保存的会话（* 表示当前会话）：\n" + strings.Join(lines, "\n")}, nil
				}
				return CommandResult{Output: fmt.Sprintf("当前可恢复上下文有 %d 条消息；完整记录有 %d 条消息。", len(a.history), len(a.transcript))}, nil
			},
		},
		{
			Name: "/resume",
			Help: "恢复指定会话，例如 /resume 7f3a91c2。",
			Run: func(_ context.Context, args []string) (CommandResult, error) {
				catalog, ok := a.session.(SessionCatalog)
				if !ok {
					return CommandResult{}, fmt.Errorf("当前会话存储不支持恢复指定会话")
				}
				if len(args) != 1 {
					return CommandResult{}, fmt.Errorf("用法: /resume <session-id>")
				}
				state, err := catalog.ResumeSession(args[0])
				if err != nil {
					return CommandResult{}, fmt.Errorf("恢复会话: %w", err)
				}
				a.history = state.Context
				a.transcript = state.Transcript
				return CommandResult{Output: fmt.Sprintf("已恢复会话 %s。", args[0])}, nil
			},
		},
		{
			Name: "/model",
			Help: "查看配置文件选择的模型供应商和模型。",
			Run: func(context.Context, []string) (CommandResult, error) {
				return CommandResult{Output: fmt.Sprintf("provider=%s, model=%s", a.config.Provider.Type, a.config.Provider.Model)}, nil
			},
		},
		{
			Name: "/exit",
			Help: "退出，不删除已保存的会话。",
			Run: func(context.Context, []string) (CommandResult, error) {
				return CommandResult{Exit: true}, nil
			},
		},
	}
	for _, command := range commands {
		if err := a.RegisterCommand(command); err != nil {
			return err
		}
	}
	return nil
}

func runREPL(ctx context.Context, application *Application, input *bufio.Reader, output io.Writer) error {
	application.SetStreamOutput(output)
	if _, err := fmt.Fprintln(output, "Coding Agent 已就绪（已开始新会话；/history 查看历史，/resume <id> 恢复，/help 查看本地命令，/exit 退出）。"); err != nil {
		return fmt.Errorf("输出启动信息: %w", err)
	}
	for {
		if _, err := fmt.Fprint(output, "\n> "); err != nil {
			return fmt.Errorf("输出提示符: %w", err)
		}
		line, readErr := input.ReadString('\n')
		if readErr != nil && len(line) == 0 {
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("读取用户输入: %w", readErr)
		}
		line = strings.TrimSpace(line)
		if line != "" {
			// A fresh context makes Ctrl-C abort only the current Agent turn. The
			// next prompt starts a new turn instead of inheriting cancellation.
			turnContext, stop := signal.NotifyContext(ctx, os.Interrupt)
			result, exit, _, err := application.HandleLine(turnContext, line)
			stop()
			if err != nil {
				if _, writeErr := fmt.Fprintf(output, "[Agent] %v\n", err); writeErr != nil {
					return fmt.Errorf("输出 Agent 错误: %w", writeErr)
				}
			} else if result != "" {
				if _, writeErr := fmt.Fprintf(output, "%s\n", result); writeErr != nil {
					return fmt.Errorf("输出 Agent 结果: %w", writeErr)
				}
			}
			if exit {
				return nil
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
}
