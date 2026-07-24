package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentgraph "agent-chat/internal/agent/graph"
	domain "agent-chat/internal/domain/chat"
)

// RunRepository 定义一次 Agent Run 执行所需的状态事务。
type RunRepository interface {
	BeginRunAttempt(context.Context, domain.BeginRunAttempt) (domain.RunSource, error)
	CompleteRun(context.Context, domain.CompleteRunCommand) error
	RecordRunFailure(context.Context, domain.RecordRunFailureCommand) error
}

// RuntimeFactory 按会话绑定的知识库创建隔离 RAG Runtime。
type RuntimeFactory interface {
	Build(context.Context, string) (agentgraph.Runner, error)
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
}

// NewExecutor 创建 Agent Run 执行用例。
func NewExecutor(
	repository RunRepository,
	factory RuntimeFactory,
	idGenerator IDGenerator,
	clock Clock,
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
	return &Executor{
		repository:  repository,
		factory:     factory,
		idGenerator: idGenerator,
		clock:       clock,
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

	runtime, err := executor.factory.Build(ctx, source.KnowledgeBaseID)
	if err != nil {
		return executor.failAttempt(
			ctx,
			request,
			"rag_runtime_build_failed",
			false,
			err,
		)
	}
	output, err := runtime.Run(ctx, agentgraph.Input{Query: source.Message.Content})
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
	messageID := executor.idGenerator.NewID("msg_")
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
		return newFailure("complete_agent_run_failed", true, err)
	}
	return nil
}

func (executor *Executor) completionEvents(
	messageID string,
	output agentgraph.Output,
	createdAt time.Time,
) []domain.EventDraft {
	events := []domain.EventDraft{
		{
			ID:   executor.idGenerator.NewID("evt_"),
			Type: domain.EventTypeRetrievalCompleted,
			Payload: map[string]any{
				"evidence": output.Assessment.Evidence,
			},
			CreatedAt: createdAt,
		},
		{
			ID:   executor.idGenerator.NewID("evt_"),
			Type: domain.EventTypeAnswerabilityDecided,
			Payload: map[string]any{
				"decision":   output.Assessment.Decision,
				"reason":     output.Assessment.Reason,
				"confidence": output.Assessment.Confidence,
				"evidence":   output.Assessment.Evidence,
			},
			CreatedAt: createdAt,
		},
		{
			ID:   executor.idGenerator.NewID("evt_"),
			Type: domain.EventTypeMessageDelta,
			Payload: map[string]any{
				"messageId": messageID,
				"delta":     output.Answer,
			},
			CreatedAt: createdAt,
		},
	}
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
