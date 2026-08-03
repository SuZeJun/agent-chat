package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	application "agent-chat/internal/application/ticket"
	domain "agent-chat/internal/domain/ticket"
)

// TicketCreationExecutor 定义 ticket.create Job 调用的 Application 用例。
type TicketCreationExecutor interface {
	Execute(context.Context, domain.ExecuteCreateCommand) (domain.Ticket, error)
}

// TicketCreateHandler 校验并执行持久化工单写操作。
type TicketCreateHandler struct {
	executor TicketCreationExecutor
}

// NewTicketCreateHandler 创建 ticket.create Job Handler。
func NewTicketCreateHandler(executor TicketCreationExecutor) (*TicketCreateHandler, error) {
	if executor == nil {
		return nil, errors.New("ticket creation executor is required")
	}
	return &TicketCreateHandler{executor: executor}, nil
}

// Handle 校验 Job 类型、幂等键和稳定 Payload 后执行工单创建。
func (handler *TicketCreateHandler) Handle(ctx context.Context, job Job) error {
	if job.Type != domain.CreateJobType {
		return Permanent("invalid_job_type", nil)
	}
	command, err := decodeTicketCreatePayload(job.Payload)
	if err != nil {
		return Permanent("invalid_job_payload", err)
	}
	if job.IdempotencyKey != command.ApprovalID {
		return Permanent("job_idempotency_mismatch", nil)
	}
	_, err = handler.executor.Execute(ctx, command)
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
	return Retryable("ticket_creation_failed", err)
}

func decodeTicketCreatePayload(raw json.RawMessage) (domain.ExecuteCreateCommand, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var command domain.ExecuteCreateCommand
	if err := decoder.Decode(&command); err != nil {
		return domain.ExecuteCreateCommand{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return domain.ExecuteCreateCommand{}, err
	}
	if err := command.Validate(); err != nil {
		return domain.ExecuteCreateCommand{}, err
	}
	return command, nil
}
