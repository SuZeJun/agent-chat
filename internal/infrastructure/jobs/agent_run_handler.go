package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"
)

// AgentRunExecutor 定义 agent.run Job 调用的 Application 用例。
type AgentRunExecutor interface {
	ExecuteRun(context.Context, application.ExecuteRunRequest) error
}

// AgentRunHandler 将持久化 Job Payload 和尝试次数转换为 Run 执行请求。
type AgentRunHandler struct {
	executor AgentRunExecutor
}

// NewAgentRunHandler 创建 agent.run Job Handler。
func NewAgentRunHandler(executor AgentRunExecutor) (*AgentRunHandler, error) {
	if executor == nil {
		return nil, errors.New("agent run executor is required")
	}
	return &AgentRunHandler{executor: executor}, nil
}

// Handle 校验任务类型、Payload 和幂等键后执行 Agent Run。
func (handler *AgentRunHandler) Handle(ctx context.Context, job Job) error {
	if job.Type != domain.AgentRunJobType {
		return Permanent("invalid_job_type", nil)
	}
	payload, err := decodeAgentRunPayload(job.Payload)
	if err != nil {
		return Permanent("invalid_job_payload", err)
	}
	if job.IdempotencyKey != "" && job.IdempotencyKey != payload.RunID {
		return Permanent("job_idempotency_mismatch", nil)
	}

	err = handler.executor.ExecuteRun(ctx, application.ExecuteRunRequest{
		RunID:       payload.RunID,
		Attempt:     job.Attempts,
		MaxAttempts: job.MaxAttempts,
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var failure *application.Failure
	if errors.As(err, &failure) {
		if failure.RetryAllowed {
			return Retryable(failure.Code, err)
		}
		return Permanent(failure.Code, err)
	}
	return Retryable("agent_run_execution_failed", err)
}

type agentRunPayload struct {
	RunID string `json:"run_id"`
}

func decodeAgentRunPayload(rawPayload json.RawMessage) (agentRunPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(rawPayload))
	decoder.DisallowUnknownFields()
	var payload agentRunPayload
	if err := decoder.Decode(&payload); err != nil {
		return agentRunPayload{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return agentRunPayload{}, err
	}
	payload.RunID = strings.TrimSpace(payload.RunID)
	if payload.RunID == "" || len(payload.RunID) > 64 {
		return agentRunPayload{}, errors.New("run ID must be 1-64 characters")
	}
	return payload, nil
}
