package main

import (
	"context"
	"testing"

	"github.com/arixbit/gofer/agent"
)

type scriptedProvider struct {
	calls int
	chat  func(*agent.ChatRequest, int) (*agent.ChatResponse, error)
}

func (p *scriptedProvider) Chat(_ context.Context, request *agent.ChatRequest) (*agent.ChatResponse, error) {
	p.calls++
	return p.chat(request, p.calls)
}

func (p *scriptedProvider) CountTokens(_ context.Context, _ []agent.Message) (int, error) {
	return 0, nil
}

type memorySessionStore struct {
	state SessionState
}

func (s *memorySessionStore) Load() (SessionState, error) {
	return s.state, nil
}

func (s *memorySessionStore) Save(state SessionState) error {
	s.state = state
	return nil
}

func newTestApplication(t *testing.T, workspace Workspace, provider agent.ModelProvider, store SessionStore, opts ...agent.AgentOption) *Application {
	t.Helper()
	registry := agent.NewToolRegistry()
	application := &Application{
		registry: registry,
		session:  store,
		config: Config{Provider: ProviderConfig{
			Type:  "deepseek",
			Model: "test-model",
		}},
		commands: NewCommandRegistry(),
	}
	for _, tool := range []agent.Tool{
		NewReadFileTool(workspace),
		NewBashTool(workspace),
		NewEditFileTool(workspace),
		NewWriteFileTool(workspace),
	} {
		if err := application.RegisterTool(tool); err != nil {
			t.Fatalf("RegisterTool(%s): %v", tool.Definition().Name, err)
		}
	}
	allOpts := append([]agent.AgentOption{agent.WithToolRegistry(registry)}, opts...)
	application.runtime = agent.NewAgent(
		provider,
		allOpts...,
	)
	if err := application.installDefaultCommands(); err != nil {
		t.Fatalf("installDefaultCommands: %v", err)
	}
	return application
}
