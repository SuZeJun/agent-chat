package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	domain "agent-chat/internal/domain/chat"
)

type fakeRepository struct {
	submission domain.StartRunSubmission
	result     domain.StartRunResult
	err        error
	calls      int
}

func (*fakeRepository) CreateConversation(
	context.Context,
	domain.Conversation,
) error {
	return nil
}

func (repository *fakeRepository) StartRun(
	_ context.Context,
	submission domain.StartRunSubmission,
) (domain.StartRunResult, error) {
	repository.calls++
	repository.submission = submission
	if repository.err != nil {
		return domain.StartRunResult{}, repository.err
	}
	if repository.result.Message.ID != "" {
		return repository.result, nil
	}
	return domain.StartRunResult{
		Message: submission.Message,
		Run:     submission.Run,
	}, nil
}

type sequentialIDGenerator struct {
	next int
}

func (generator *sequentialIDGenerator) NewID(prefix string) string {
	generator.next++
	return fmt.Sprintf("%s%d", prefix, generator.next)
}

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}

func TestSendMessageBuildsAtomicSubmission(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	repository := &fakeRepository{}
	service := newTestService(t, repository, now)

	result, err := service.SendMessage(context.Background(), Request{
		CustomerID:      " customer-1 ",
		ConversationID:  " conversation-1 ",
		ClientMessageID: " client-message-1 ",
		Content:         " 如何重置密码？ ",
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	submission := repository.submission
	if submission.CustomerID != "customer-1" ||
		submission.Message.ID != "msg_1" ||
		submission.Message.ConversationID != "conversation-1" ||
		submission.Message.ClientMessageID != "client-message-1" ||
		submission.Message.Content != "如何重置密码？" ||
		submission.Run.ID != "run_2" ||
		submission.Run.SourceMessageID != "msg_1" ||
		submission.Event.ID != "evt_3" ||
		submission.Event.RunID != "run_2" ||
		submission.JobID != "job_4" {
		t.Fatalf("unexpected submission: %#v", submission)
	}
	if !submission.Message.CreatedAt.Equal(now.UTC()) ||
		!submission.Run.CreatedAt.Equal(now.UTC()) ||
		!submission.Event.CreatedAt.Equal(now.UTC()) {
		t.Fatalf("timestamps were not normalized to UTC: %#v", submission)
	}
	if result.MessageID != "msg_1" ||
		result.RunID != "run_2" ||
		result.RunStatus != domain.RunStatusPending ||
		result.Duplicate {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSendMessageReturnsExistingIdempotentResult(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeRepository{
		result: domain.StartRunResult{
			Message: domain.Message{
				ID:              "existing-message",
				ConversationID:  "conversation-1",
				ClientMessageID: "client-message-1",
				Role:            domain.MessageRoleCustomer,
				Content:         "问题",
				CreatedAt:       now.Add(-time.Minute),
			},
			Run: domain.AgentRun{
				ID:              "existing-run",
				ConversationID:  "conversation-1",
				SourceMessageID: "existing-message",
				Status:          domain.RunStatusRunning,
				CreatedAt:       now.Add(-time.Minute),
				UpdatedAt:       now,
			},
			Duplicate: true,
		},
	}
	service := newTestService(t, repository, now)

	result, err := service.SendMessage(context.Background(), Request{
		CustomerID:      "customer-1",
		ConversationID:  "conversation-1",
		ClientMessageID: "client-message-1",
		Content:         "问题",
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if result.MessageID != "existing-message" ||
		result.RunID != "existing-run" ||
		result.RunStatus != domain.RunStatusRunning ||
		!result.Duplicate {
		t.Fatalf("unexpected replay result: %#v", result)
	}
}

func TestSendMessageRejectsInvalidRequestBeforeRepository(t *testing.T) {
	tests := []Request{
		{},
		{
			CustomerID:      "customer-1",
			ConversationID:  "conversation-1",
			ClientMessageID: "client-message-1",
			Content:         "   ",
		},
	}
	for index, request := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			repository := &fakeRepository{}
			service := newTestService(t, repository, time.Now())

			_, err := service.SendMessage(context.Background(), request)
			assertFailure(t, err, "invalid_send_message", false)
			if repository.calls != 0 {
				t.Fatal("invalid request reached repository")
			}
		})
	}
}

func TestSendMessageMapsRepositoryFailures(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		code      string
		retryable bool
	}{
		{
			name: "not found",
			err:  domain.ErrNotFound,
			code: "conversation_not_found",
		},
		{
			name: "inactive conversation",
			err:  domain.ErrInvalidState,
			code: "conversation_not_ai_active",
		},
		{
			name: "idempotency conflict",
			err:  domain.ErrIdempotencyConflict,
			code: "client_message_id_conflict",
		},
		{
			name:      "write conflict",
			err:       domain.ErrConflict,
			code:      "send_message_conflict",
			retryable: true,
		},
		{
			name:      "unknown database failure",
			err:       errors.New("sensitive database response"),
			code:      "send_message_failed",
			retryable: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{err: test.err}
			service := newTestService(t, repository, time.Now())

			_, err := service.SendMessage(context.Background(), Request{
				CustomerID:      "customer-1",
				ConversationID:  "conversation-1",
				ClientMessageID: "client-message-1",
				Content:         "问题",
			})
			assertFailure(t, err, test.code, test.retryable)
			if err.Error() != test.code {
				t.Fatalf("error leaked repository details: %v", err)
			}
		})
	}
}

func newTestService(
	t *testing.T,
	repository MessageRepository,
	now time.Time,
) *Service {
	t.Helper()
	service, err := NewService(
		repository,
		&sequentialIDGenerator{},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	return service
}

func assertFailure(
	t *testing.T,
	err error,
	code string,
	retryable bool,
) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) ||
		failure.Code != code ||
		failure.RetryAllowed != retryable {
		t.Fatalf("unexpected failure: %v", err)
	}
}
