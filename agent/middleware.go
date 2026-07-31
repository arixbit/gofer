package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Middleware 中间件——包裹 Run 方法的函数
type Middleware func(next Agent) Agent

// middlewareAgent 中间件适配器——实现 Agent 接口
type middlewareAgent struct {
	handler func(ctx context.Context, req Request) (*Response, error)
}

func (m *middlewareAgent) Run(ctx context.Context, req Request) (*Response, error) {
	return m.handler(ctx, req)
}

// LoggingMiddleware 日志中间件——记录每次调用的输入输出
func LoggingMiddleware() Middleware {
	return func(next Agent) Agent {
		return &middlewareAgent{
			handler: func(ctx context.Context, req Request) (*Response, error) {
				start := time.Now()
				fmt.Printf("[Agent] 开始处理: %s\n", req.Message)

				resp, err := next.Run(ctx, req)
				elapsed := time.Since(start)

				if err != nil {
					fmt.Printf("[Agent] 错误: %v (耗时 %v)\n", err, elapsed)
				} else {
					fmt.Printf("[Agent] 完成 (耗时 %v, 输入 %d token, 输出 %d token)\n",
						elapsed, resp.InputTokens, resp.OutputTokens)
				}
				return resp, err
			},
		}
	}
}

// RateLimitMiddleware 限流中间件——控制每分钟的最大调用次数
func RateLimitMiddleware(perMinute int) Middleware {
	var mu sync.Mutex
	var timestamps []time.Time

	return func(next Agent) Agent {
		return &middlewareAgent{
			handler: func(ctx context.Context, req Request) (*Response, error) {
				mu.Lock()
				now := time.Now()
				// 清除过期的记录
				cutoff := now.Add(-time.Minute)
				n := 0
				for _, ts := range timestamps {
					if !ts.Before(cutoff) {
						timestamps[n] = ts
						n++
					}
				}
				timestamps = timestamps[:n]

				if len(timestamps) >= perMinute {
					mu.Unlock()
					return nil, fmt.Errorf("调用频率超限，请稍后重试")
				}
				timestamps = append(timestamps, now)
				mu.Unlock()

				return next.Run(ctx, req)
			},
		}
	}
}
