package graph

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
)

// traceStartKey 用于在 Eino Callback Context 中关联同一次调用的开始时间。
type traceStartKey struct{}

// traceStart 保存单个节点或模型调用的开始时间。
type traceStart struct {
	startedAt time.Time
}

// traceCollector 并发安全地收集一次 Graph Run 的脱敏步骤。
type traceCollector struct {
	mutex sync.Mutex
	steps []TraceStep
}

// newTraceCollector 为每次 Run 创建独立收集器，避免跨请求混合 Trace。
func newTraceCollector() *traceCollector {
	return &traceCollector{steps: make([]TraceStep, 0, 8)}
}

// handler 只记录开始、结束、错误和 Token，不读取 Prompt 或原始错误。
func (collector *traceCollector) handler() callbacks.Handler {
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(
			ctx context.Context,
			info *callbacks.RunInfo,
			_ callbacks.CallbackInput,
		) context.Context {
			if !traceableRunInfo(info) {
				return ctx
			}
			return context.WithValue(ctx, traceStartKey{}, traceStart{
				startedAt: time.Now().UTC(),
			})
		}).
		OnEndFn(func(
			ctx context.Context,
			info *callbacks.RunInfo,
			output callbacks.CallbackOutput,
		) context.Context {
			collector.finish(ctx, info, output, "completed")
			return ctx
		}).
		OnErrorFn(func(
			ctx context.Context,
			info *callbacks.RunInfo,
			_ error,
		) context.Context {
			collector.finish(ctx, info, nil, "failed")
			return ctx
		}).
		Build()
}

// finish 将 Eino 回调转换为稳定 TraceStep，并按组件类型提取受控指标。
func (collector *traceCollector) finish(
	ctx context.Context,
	info *callbacks.RunInfo,
	output callbacks.CallbackOutput,
	status string,
) {
	if !traceableRunInfo(info) {
		return
	}
	start, ok := ctx.Value(traceStartKey{}).(traceStart)
	if !ok {
		return
	}
	completedAt := time.Now().UTC()
	step := TraceStep{
		Name:           info.Name,
		Component:      string(info.Component),
		ComponentType:  info.Type,
		Status:         status,
		StartedAt:      start.startedAt,
		CompletedAt:    completedAt,
		DurationMillis: completedAt.Sub(start.startedAt).Milliseconds(),
	}
	if info.Component == components.ComponentOfChatModel {
		modelOutput := einomodel.ConvCallbackOutput(output)
		if modelOutput != nil && modelOutput.TokenUsage != nil {
			step.PromptTokens = modelOutput.TokenUsage.PromptTokens
			step.CompletionTokens = modelOutput.TokenUsage.CompletionTokens
		}
	}
	collector.mutex.Lock()
	collector.steps = append(collector.steps, step)
	collector.mutex.Unlock()
}

// snapshot 返回按开始时间排序的副本，调用方不能修改收集器内部状态。
func (collector *traceCollector) snapshot() []TraceStep {
	collector.mutex.Lock()
	steps := append([]TraceStep(nil), collector.steps...)
	collector.mutex.Unlock()
	sort.SliceStable(steps, func(left int, right int) bool {
		return steps[left].StartedAt.Before(steps[right].StartedAt)
	})
	return steps
}

// traceableRunInfo 只允许 Graph Lambda 和 ChatModel 进入持久化 Trace。
func traceableRunInfo(info *callbacks.RunInfo) bool {
	if info == nil {
		return false
	}
	return info.Component == compose.ComponentOfLambda ||
		info.Component == components.ComponentOfChatModel
}
