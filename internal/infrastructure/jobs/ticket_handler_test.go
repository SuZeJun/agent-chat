package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	application "agent-chat/internal/application/ticket"
	domain "agent-chat/internal/domain/ticket"
)

type fakeTicketCreationExecutor struct {
	command domain.ExecuteCreateCommand
	err     error
	calls   int
}

func (executor *fakeTicketCreationExecutor) Execute(
	_ context.Context,
	command domain.ExecuteCreateCommand,
) (domain.Ticket, error) {
	executor.calls++
	executor.command = command
	return domain.Ticket{}, executor.err
}

func ticketCreateJob(t *testing.T) Job {
	t.Helper()
	command := domain.ExecuteCreateCommand{
		ApprovalID:   "approval-1",
		TicketID:     "ticket-1",
		TicketNumber: "TK-1",
		EventID:      "event-1",
		CreatedAt:    time.Now().UTC(),
	}
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	job := testJob("job-1", domain.CreateJobType, 1)
	job.IdempotencyKey = command.ApprovalID
	job.Payload = payload
	return job
}

func TestTicketCreateHandlerDispatchesStablePayload(t *testing.T) {
	executor := &fakeTicketCreationExecutor{}
	handler, err := NewTicketCreateHandler(executor)
	if err != nil {
		t.Fatalf("NewTicketCreateHandler returned error: %v", err)
	}
	job := ticketCreateJob(t)
	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if executor.calls != 1 || executor.command.ApprovalID != job.IdempotencyKey ||
		executor.command.TicketNumber != "TK-1" {
		t.Fatalf("unexpected dispatch: %#v", executor.command)
	}
}

func TestTicketCreateHandlerRejectsIdempotencyMismatch(t *testing.T) {
	executor := &fakeTicketCreationExecutor{}
	handler, _ := NewTicketCreateHandler(executor)
	job := ticketCreateJob(t)
	job.IdempotencyKey = "other-approval"
	err := handler.Handle(context.Background(), job)
	var handlerError *HandlerError
	if !errors.As(err, &handlerError) || handlerError.code != "job_idempotency_mismatch" ||
		handlerError.retryable {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if executor.calls != 0 {
		t.Fatal("invalid job reached executor")
	}
}

func TestTicketCreateHandlerPreservesRetryability(t *testing.T) {
	executor := &fakeTicketCreationExecutor{
		err: &application.Failure{Code: "create_ticket_failed", RetryAllowed: true},
	}
	handler, _ := NewTicketCreateHandler(executor)
	err := handler.Handle(context.Background(), ticketCreateJob(t))
	var handlerError *HandlerError
	if !errors.As(err, &handlerError) || handlerError.code != "create_ticket_failed" ||
		!handlerError.retryable {
		t.Fatalf("unexpected handler error: %v", err)
	}
}
