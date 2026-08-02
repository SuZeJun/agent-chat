package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	agentgraph "agent-chat/internal/agent/graph"
	domain "agent-chat/internal/domain/chat"
)

// RunRepository 定义一次 Agent Run 执行所需的状态事务。
type RunRepository interface {
	BeginRunAttempt(context.Context, domain.BeginRunAttempt) (domain.RunSource, error)
	AppendRunProgress(context.Context, domain.AppendRunProgressCommand) error
	CompleteRun(context.Context, domain.CompleteRunCommand) error
	RecordRunFailure(context.Context, domain.RecordRunFailureCommand) error
}

// RuntimeFactory 按会话绑定的知识库创建隔离 RAG Runtime。
type RuntimeFactory interface {
	// Build 接收知识库 ID 与客户 ID：两者都是服务端从会话推导的授权作用域，
	// Runtime 内的检索器与工具据此绑定，运行期不可更改。
	Build(ctx context.Context, knowledgeBaseID string, customerID string) (agentgraph.Runner, error)
}

// ExecuteRunRequest 包含 Worker 当前持有的任务尝试信息。
type ExecuteRunRequest struct {
	RunID       string
	Attempt     int
	MaxAttempts int
}

// Executor 编排 Run 状态转换、RAG Graph 和最终持久化。
type Executor struct {
	repository  RunRepository
	factory     RuntimeFactory
	idGenerator IDGenerator
	clock       Clock
	logger      *slog.Logger
}

// NewExecutor 创建 Agent Run 执行用例。
//
// logger 仅用于记录尽力而为的进度投递失败；这类失败不会中断 Run，因此不能
// 通过返回值传播，只能落到日志。
func NewExecutor(
	repository RunRepository,
	factory RuntimeFactory,
	idGenerator IDGenerator,
	clock Clock,
	logger *slog.Logger,
) (*Executor, error) {
	if repository == nil {
		return nil, errors.New("run repository is required")
	}
	if factory == nil {
		return nil, errors.New("RAG runtime factory is required")
	}
	if idGenerator == nil {
		return nil, errors.New("run ID generator is required")
	}
	if clock == nil {
		return nil, errors.New("run clock is required")
	}
	if logger == nil {
		return nil, errors.New("run logger is required")
	}
	return &Executor{
		repository:  repository,
		factory:     factory,
		idGenerator: idGenerator,
		clock:       clock,
		logger:      logger,
	}, nil
}

// ExecuteRun 执行一次持久化 Job 尝试，并保证终态结果与事件原子提交。
func (executor *Executor) ExecuteRun(
	ctx context.Context,
	request ExecuteRunRequest,
) error {
	request.RunID = strings.TrimSpace(request.RunID)
	if request.RunID == "" ||
		len(request.RunID) > maxScopedIDLength ||
		request.Attempt <= 0 ||
		request.MaxAttempts <= 0 ||
		request.Attempt > request.MaxAttempts {
		return newFailure(
			"invalid_execute_run_request",
			false,
			errors.New("run ID or attempt is invalid"),
		)
	}

	startedAt := executor.clock.Now().UTC()
	source, err := executor.repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   request.RunID,
		Attempt: request.Attempt,
		Event: domain.EventDraft{
			ID:   executor.idGenerator.NewID("evt_"),
			Type: domain.EventTypeRunStarted,
			Payload: map[string]any{
				"attempt": request.Attempt,
			},
			CreatedAt: startedAt,
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if errors.Is(err, domain.ErrInvalidState) {
			return executor.failAttempt(
				ctx,
				request,
				"conversation_not_ai_active",
				false,
				err,
			)
		}
		if errors.Is(err, domain.ErrNotFound) {
			return newFailure("agent_run_not_found", false, err)
		}
		return newFailure("begin_agent_run_failed", true, err)
	}
	if source.Terminal() {
		return nil
	}

	runtime, err := executor.factory.Build(ctx, source.KnowledgeBaseID, source.CustomerID)
	if err != nil {
		return executor.failAttempt(
			ctx,
			request,
			"rag_runtime_build_failed",
			false,
			err,
		)
	}
	// 消息 ID 必须在生成开始前分配：回答增量在运行期就要引用它。
	messageID := executor.idGenerator.NewID("msg_")
	progress := newRunProgress(
		executor.repository,
		executor.idGenerator,
		executor.clock,
		executor.logger,
		request.RunID,
		messageID,
	)

	output, err := runtime.Run(
		agentgraph.WithObserver(ctx, progress),
		agentgraph.Input{Query: source.Message.Content},
	)
	// 失败时也要送出残余增量：已经产生的内容对排查有价值，重试会以
	// run.started 为界让消费方重置，不会与新一次尝试的内容混在一起。
	progress.Flush(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		code := "rag_execution_failed"
		retryable := true
		var failure *agentgraph.Failure
		if errors.As(err, &failure) {
			code = failure.Code
			retryable = failure.RetryAllowed
		}
		return executor.failAttempt(ctx, request, code, retryable, err)
	}

	completedAt := executor.clock.Now().UTC()
	result, err := graphResult(output)
	if err != nil {
		return executor.failAttempt(
			ctx,
			request,
			"invalid_graph_result",
			false,
			err,
		)
	}
	command := domain.CompleteRunCommand{
		RunID: request.RunID,
		Message: domain.Message{
			ID:             messageID,
			ConversationID: source.Run.ConversationID,
			AgentRunID:     request.RunID,
			Role:           domain.MessageRoleAssistant,
			Content:        output.Answer,
			CreatedAt:      completedAt,
		},
		Result:      result,
		Events:      executor.completionEvents(messageID, output, completedAt),
		Steps:       graphTraceSteps(output.Trace),
		CompletedAt: completedAt,
	}
	if err := executor.repository.CompleteRun(ctx, command); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if errors.Is(err, domain.ErrInvalidState) {
			return executor.failAttempt(
				ctx,
				request,
				"conversation_not_ai_active",
				false,
				err,
			)
		}
		// 命令不满足领域契约属于确定性缺陷，重试只会重复失败并推迟 Run 终态。
		if errors.Is(err, domain.ErrInvalidCommand) {
			return executor.failAttempt(
				ctx,
				request,
				"invalid_agent_run_completion",
				false,
				err,
			)
		}
		// 必须经由 failAttempt 收敛 Run，否则 Job 耗尽重试后 Run 会永远停在 running。
		return executor.failAttempt(
			ctx,
			request,
			"complete_agent_run_failed",
			true,
			err,
		)
	}
	return nil
}

// graphTraceSteps 将 Agent 层 Trace 映射为不依赖 Eino 的 Domain 持久化契约。
func graphTraceSteps(trace []agentgraph.TraceStep) []domain.RunStepDraft {
	steps := make([]domain.RunStepDraft, len(trace))
	for index, step := range trace {
		steps[index] = domain.RunStepDraft{
			Name:             step.Name,
			Component:        step.Component,
			ComponentType:    step.ComponentType,
			Status:           step.Status,
			StartedAt:        step.StartedAt,
			CompletedAt:      step.CompletedAt,
			DurationMillis:   step.DurationMillis,
			PromptTokens:     step.PromptTokens,
			CompletionTokens: step.CompletionTokens,
		}
	}
	return steps
}

// completionEvents 生成引用和终态事件。
//
// 检索、决策与回答增量已在运行期经 Observer 发出，此处不再重复；否则客户端会
// 收到两份内容，累积出的回答也会翻倍。
func (executor *Executor) completionEvents(
	messageID string,
	output agentgraph.Output,
	createdAt time.Time,
) []domain.EventDraft {
	events := make([]domain.EventDraft, 0, len(output.Citations)+1)
	for _, citation := range output.Citations {
		events = append(events, domain.EventDraft{
			ID:   executor.idGenerator.NewID("evt_"),
			Type: domain.EventTypeMessageCitation,
			Payload: map[string]any{
				"messageId": messageID,
				"citation":  citation,
			},
			CreatedAt: createdAt,
		})
	}
	events = append(events, domain.EventDraft{
		ID:   executor.idGenerator.NewID("evt_"),
		Type: domain.EventTypeRunCompleted,
		Payload: map[string]any{
			"status":     domain.RunStatusCompleted,
			"messageId":  messageID,
			"nodePath":   output.NodePath,
			"nextAction": output.NextAction,
		},
		CreatedAt: createdAt,
	})
	return events
}

// failAttempt 记录稳定错误码；仅永久错误或最后一次尝试会结束 Run。
func (executor *Executor) failAttempt(
	ctx context.Context,
	request ExecuteRunRequest,
	code string,
	retryable bool,
	cause error,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	terminal := !retryable || request.Attempt >= request.MaxAttempts
	eventType := domain.EventTypeRunStatus
	status := domain.RunStatusRunning
	if terminal {
		eventType = domain.EventTypeRunFailed
		status = domain.RunStatusFailed
	}
	occurredAt := executor.clock.Now().UTC()
	err := executor.repository.RecordRunFailure(
		ctx,
		domain.RecordRunFailureCommand{
			RunID:     request.RunID,
			Attempt:   request.Attempt,
			ErrorCode: code,
			Terminal:  terminal,
			Event: domain.EventDraft{
				ID:   executor.idGenerator.NewID("evt_"),
				Type: eventType,
				Payload: map[string]any{
					"status":    status,
					"attempt":   request.Attempt,
					"errorCode": code,
					"retrying":  !terminal,
				},
				CreatedAt: occurredAt,
			},
			OccurredAt: occurredAt,
		},
	)
	if err != nil {
		return newFailure("record_agent_run_failure_failed", true, err)
	}
	return newFailure(code, retryable, cause)
}

// graphResult 通过 JSON 边界生成可安全持久化和返回的 Graph 快照。
func graphResult(output agentgraph.Output) (map[string]any, error) {
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("encode graph output: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("decode graph output: %w", err)
	}
	return result, nil
}
