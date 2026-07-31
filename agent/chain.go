package agent

import (
	"context"
	"fmt"
)

// Chain 多 Agent 串行编排
type Chain struct {
	steps []ChainStep
}

// NewChain 创建 Chain
func NewChain(steps ...ChainStep) *Chain {
	return &Chain{steps: steps}
}

// ChainStep 是 Chain 中的一步
type ChainStep interface {
	// Process 执行这一步
	// 返回值中 Done=true 表示链到此结束，不需要继续
	Process(ctx context.Context, req Request) (*StepResult, error)
}

// StepResult 是 ChainStep 的执行结果
type StepResult struct {
	Text    string
	History []Message
	Done    bool // 是否终止链
}

// agentChainStep 将 Agent 适配为 ChainStep
type agentChainStep struct {
	agent Agent
}

func (s *agentChainStep) Process(ctx context.Context, req Request) (*StepResult, error) {
	resp, err := s.agent.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return &StepResult{
		Text:    resp.Text,
		History: resp.History,
		Done:    true, // 默认 Agent 执行完就结束
	}, nil
}

// Execute 执行 Chain
func (c *Chain) Execute(ctx context.Context, req Request) (*StepResult, error) {
	currentReq := req

	for _, step := range c.steps {
		result, err := step.Process(ctx, currentReq)
		if err != nil {
			return nil, fmt.Errorf("Chain 执行失败: %w", err)
		}
		if result.Done {
			return result, nil
		}
		// 下一步的输入 = 上一步的输出
		currentReq = Request{
			Message: result.Text,
			History: result.History,
		}
	}

	return &StepResult{Text: "", Done: true}, nil
}
