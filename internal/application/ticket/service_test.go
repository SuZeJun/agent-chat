package ticketapp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	domain "agent-chat/internal/domain/ticket"
)

type fakeTicketRepository struct {
	confirmCommand domain.ConfirmCommand
	confirmResult  domain.ApproveResult
	confirmErr     error
	cancelEventID  string
	cancelResult   domain.Approval
	loadApproval   domain.Approval
	loadTicket     domain.Ticket
	executed       domain.ExecuteCreateCommand
	createCalls    int
}

func (repository *fakeTicketRepository) CreateApproval(context.Context, domain.Approval) error {
	return nil
}
func (repository *fakeTicketRepository) LoadApproval(
	context.Context,
	string,
	string,
) (domain.Approval, error) {
	return repository.loadApproval, nil
}
func (repository *fakeTicketRepository) ConfirmAndEnqueue(
	_ context.Context,
	command domain.ConfirmCommand,
) (domain.ApproveResult, error) {
	repository.confirmCommand = command
	return repository.confirmResult, repository.confirmErr
}
func (repository *fakeTicketRepository) Cancel(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	_ time.Time,
) (domain.Approval, error) {
	repository.cancelEventID = eventID
	return repository.cancelResult, nil
}
func (repository *fakeTicketRepository) CreateTicket(
	context.Context,
	domain.Ticket,
) (domain.Ticket, error) {
	repository.createCalls++
	return domain.Ticket{}, nil
}
func (repository *fakeTicketRepository) ExecuteCreateTicket(
	_ context.Context,
	command domain.ExecuteCreateCommand,
) (domain.Ticket, error) {
	repository.executed = command
	return repository.loadTicket, nil
}
func (repository *fakeTicketRepository) LoadTicketByApproval(
	context.Context,
	string,
) (domain.Ticket, error) {
	return repository.loadTicket, nil
}

type testIDGenerator struct{ next int }

func (generator *testIDGenerator) NewID(prefix string) string {
	generator.next++
	return fmt.Sprintf("%s%d", prefix, generator.next)
}

type testClock struct{ now time.Time }

func (clock testClock) Now() time.Time { return clock.now }

func testApproval(status domain.ApprovalStatus, now time.Time) domain.Approval {
	decidedAt := now
	approval := domain.Approval{
		ID:             "approval-1",
		ConversationID: "conversation-1",
		CustomerID:     "customer-1",
		AgentRunID:     "run-1",
		Draft: domain.Draft{
			Title:       "无法导出账单",
			Description: "点击导出按钮没有反应。",
			Priority:    domain.PriorityNormal,
		},
		Status:         status,
		IdempotencyKey: domain.DeriveIdempotencyKey("run-1"),
		CreatedAt:      now.Add(-time.Minute),
		ExpiresAt:      now.Add(time.Minute),
	}
	if status != domain.ApprovalStatusPending {
		approval.DecidedAt = &decidedAt
	}
	return approval
}

func TestConfirmOnlyEnqueuesPersistentCreation(t *testing.T) {
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	approval := testApproval(domain.ApprovalStatusApproved, now)
	repository := &fakeTicketRepository{
		confirmResult: domain.ApproveResult{Approval: approval},
	}
	service, err := NewService(repository, &testIDGenerator{}, testClock{now: now})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	decision, err := service.Confirm(context.Background(), " customer-1 ", " approval-1 ")
	if err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}
	if decision.Ticket != nil || repository.createCalls != 0 {
		t.Fatal("confirmation executed the write synchronously")
	}
	command := repository.confirmCommand
	if command.CustomerID != "customer-1" || command.ApprovalID != "approval-1" ||
		command.JobID == "" || command.TicketID == "" || command.EventID == "" ||
		command.TicketEventID == "" || command.EventID == command.TicketEventID {
		t.Fatalf("confirmation did not create stable job identities: %#v", command)
	}
}

func TestRepeatedConfirmReturnsFirstTicket(t *testing.T) {
	now := time.Now().UTC()
	approval := testApproval(domain.ApprovalStatusApproved, now)
	ticket := domain.Ticket{
		ID:             "ticket-1",
		Number:         "TK-1",
		ConversationID: approval.ConversationID,
		CustomerID:     approval.CustomerID,
		ApprovalID:     approval.ID,
		Draft:          approval.Draft,
		CreatedAt:      now,
	}
	repository := &fakeTicketRepository{
		confirmResult: domain.ApproveResult{
			Approval:        approval,
			AlreadyApproved: true,
			Ticket:          &ticket,
		},
	}
	service, _ := NewService(repository, &testIDGenerator{}, testClock{now: now})
	decision, err := service.Confirm(context.Background(), approval.CustomerID, approval.ID)
	if err != nil || decision.Ticket == nil || decision.Ticket.Number != ticket.Number {
		t.Fatalf("repeated confirmation lost first result: decision=%#v err=%v", decision, err)
	}
}

func TestConfirmMapsExpiredApproval(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeTicketRepository{confirmErr: domain.ErrExpired}
	service, _ := NewService(repository, &testIDGenerator{}, testClock{now: now})
	_, err := service.Confirm(context.Background(), "customer-1", "approval-1")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "ticket_approval_expired" {
		t.Fatalf("unexpected failure: %v", err)
	}
}

func TestGetReturnsTicketAfterWorkerCompletion(t *testing.T) {
	now := time.Now().UTC()
	approval := testApproval(domain.ApprovalStatusApproved, now)
	approval.TicketID = "ticket-1"
	repository := &fakeTicketRepository{
		loadApproval: approval,
		loadTicket: domain.Ticket{
			ID:             "ticket-1",
			Number:         "TK-1",
			ConversationID: approval.ConversationID,
			CustomerID:     approval.CustomerID,
			ApprovalID:     approval.ID,
			Draft:          approval.Draft,
			CreatedAt:      now,
		},
	}
	service, _ := NewService(repository, &testIDGenerator{}, testClock{now: now})
	decision, err := service.Get(context.Background(), approval.CustomerID, approval.ID)
	if err != nil || decision.Ticket == nil || decision.Ticket.Number != "TK-1" {
		t.Fatalf("Get did not return completed ticket: decision=%#v err=%v", decision, err)
	}
}
