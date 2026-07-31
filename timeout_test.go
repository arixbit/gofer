package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arixbit/gofer/agent"
)

func TestModelTimeoutProviderBoundsEachRequest(t *testing.T) {
	provider := newModelTimeoutProvider(blockingProvider{}, 5*time.Millisecond)
	_, err := provider.Chat(context.Background(), &agent.ChatRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Chat() error = %v, want deadline exceeded", err)
	}
}

type blockingProvider struct{}

func (blockingProvider) Chat(ctx context.Context, _ *agent.ChatRequest) (*agent.ChatResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingProvider) CountTokens(context.Context, []agent.Message) (int, error) {
	return 0, nil
}
