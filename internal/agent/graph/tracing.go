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

type traceStartKey struct{}

type traceStart struct {
	startedAt time.Time
}

type traceCollector struct {
	mutex sync.Mutex
	steps []TraceStep
}

func newTraceCollector() *traceCollector {
	return &traceCollector{steps: make([]TraceStep, 0, 8)}
}

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

func (collector *traceCollector) snapshot() []TraceStep {
	collector.mutex.Lock()
	steps := append([]TraceStep(nil), collector.steps...)
	collector.mutex.Unlock()
	sort.SliceStable(steps, func(left int, right int) bool {
		return steps[left].StartedAt.Before(steps[right].StartedAt)
	})
	return steps
}

func traceableRunInfo(info *callbacks.RunInfo) bool {
	if info == nil {
		return false
	}
	return info.Component == compose.ComponentOfLambda ||
		info.Component == components.ComponentOfChatModel
}
