package graph

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Trace 字段上限与 Domain RunStepDraft 的持久化契约保持一致，
// 保证 Graph 产出的 Trace 永远不会在 Run 提交阶段才被拒绝。
const (
	maxTraceNameBytes          = 100
	maxTraceComponentTypeBytes = 255
)

// traceStartKey 用于在 Eino Callback Context 中关联同一次调用的开始时间。
type traceStartKey struct{}

// traceStart 保存单个节点或模型调用的开始时间。
type traceStart struct {
	startedAt time.Time
}

// traceCollector 并发安全地收集一次 Graph Run 的脱敏步骤。
//
// 流式输出的步骤要读完流副本才能拿到 Token 用量，因此用 WaitGroup 跟踪在途的
// 消费协程，snapshot 必须等待它们结束，否则模型步骤会随机缺失。
type traceCollector struct {
	mutex   sync.Mutex
	steps   []TraceStep
	pending sync.WaitGroup
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
		OnEndWithStreamOutputFn(func(
			ctx context.Context,
			info *callbacks.RunInfo,
			output *schema.StreamReader[callbacks.CallbackOutput],
		) context.Context {
			collector.finishStream(ctx, info, output)
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

// finishStream 消费流副本以获取 Token 用量，随后记录步骤。
//
// 受知识约束的回答走流式，Eino 此时触发的是 OnEndWithStreamOutput 而非 OnEnd；
// 若不在此处记录，模型调用会从 Trace 中彻底消失。回调持有的是私有副本，必须读完
// 并关闭，否则会泄漏协程与内存。
func (collector *traceCollector) finishStream(
	ctx context.Context,
	info *callbacks.RunInfo,
	output *schema.StreamReader[callbacks.CallbackOutput],
) {
	if output == nil {
		return
	}
	if !traceableRunInfo(info) {
		output.Close()
		return
	}
	start, ok := ctx.Value(traceStartKey{}).(traceStart)
	if !ok {
		output.Close()
		return
	}

	collector.pending.Add(1)
	go func() {
		defer collector.pending.Done()
		defer output.Close()

		var usage *einomodel.TokenUsage
		for {
			chunk, err := output.Recv()
			if err != nil {
				break
			}
			// 用量随最后一个分块返回，因此持续覆盖到流结束。
			if converted := einomodel.ConvCallbackOutput(chunk); converted != nil &&
				converted.TokenUsage != nil {
				usage = converted.TokenUsage
			}
		}

		completedAt := time.Now().UTC()
		step := TraceStep{
			Name:           traceStepName(info),
			Component:      string(info.Component),
			ComponentType:  truncateTraceField(info.Type, maxTraceComponentTypeBytes),
			Status:         "completed",
			StartedAt:      start.startedAt,
			CompletedAt:    completedAt,
			DurationMillis: completedAt.Sub(start.startedAt).Milliseconds(),
		}
		if usage != nil {
			step.PromptTokens = usage.PromptTokens
			step.CompletionTokens = usage.CompletionTokens
		}
		collector.mutex.Lock()
		collector.steps = append(collector.steps, step)
		collector.mutex.Unlock()
	}()
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
		Name:           traceStepName(info),
		Component:      string(info.Component),
		ComponentType:  truncateTraceField(info.Type, maxTraceComponentTypeBytes),
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
//
// 先等待流式步骤的消费协程结束：它们要读完流才能拿到用量，若不等待，
// 模型步骤会因竞态而随机缺失。
func (collector *traceCollector) snapshot() []TraceStep {
	collector.pending.Wait()
	collector.mutex.Lock()
	steps := append([]TraceStep(nil), collector.steps...)
	collector.mutex.Unlock()
	sort.SliceStable(steps, func(left int, right int) bool {
		return steps[left].StartedAt.Before(steps[right].StartedAt)
	})
	return steps
}

// traceStepName 返回稳定且非空的步骤名称。
//
// Eino 只为 Graph 节点填充 RunInfo.Name；在 Lambda 内部直接调用的组件（例如
// ChatModel）拿到的是空名称，而持久化契约要求名称非空，因此回退到组件身份。
func traceStepName(info *callbacks.RunInfo) string {
	if name := strings.TrimSpace(info.Name); name != "" {
		return truncateTraceField(name, maxTraceNameBytes)
	}
	return truncateTraceField(strings.TrimSpace(string(info.Component)), maxTraceNameBytes)
}

// truncateTraceField 按字节上限截断并保持 UTF-8 完整，避免超长身份导致提交失败。
func truncateTraceField(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	truncated := value[:limit]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

// traceableRunInfo 只允许 Graph Lambda 和 ChatModel 进入持久化 Trace。
func traceableRunInfo(info *callbacks.RunInfo) bool {
	if info == nil {
		return false
	}
	return info.Component == compose.ComponentOfLambda ||
		info.Component == components.ComponentOfChatModel
}
