package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "agent-chat/internal/domain/chat"
)

type fakeConversationRepository struct {
	conversation domain.Conversation
	err          error
}

func (repository *fakeConversationRepository) CreateConversation(
	_ context.Context,
	conversation domain.Conversation,
) error {
	repository.conversation = conversation
	return repository.err
}

type fakeEventRepository struct {
	customerID    string
	runID         string
	afterSequence int
	limit         int
	page          domain.RunEventPage
	err           error
}

func (repository *fakeEventRepository) LoadRunEvents(
	_ context.Context,
	customerID string,
	runID string,
	afterSequence int,
	limit int,
) (domain.RunEventPage, error) {
	repository.customerID = customerID
	repository.runID = runID
	repository.afterSequence = afterSequence
	repository.limit = limit
	return repository.page, repository.err
}

func TestCreateConversation(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	repository := &fakeConversationRepository{}
	service, err := NewConversationService(
		repository,
		&sequentialIDGenerator{},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatalf("NewConversationService: %v", err)
	}
	result, err := service.Create(context.Background(), CreateConversationRequest{
		CustomerID:      " customer-1 ",
		KnowledgeBaseID: " base-1 ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.ID == "" ||
		result.KnowledgeBaseID != "base-1" ||
		result.Status != domain.ConversationStatusAIActive ||
		repository.conversation.CustomerID != "customer-1" ||
		repository.conversation.CreatedAt != now {
		t.Fatalf("unexpected conversation: result=%#v persisted=%#v", result, repository.conversation)
	}
}

func TestCreateConversationMapsMissingKnowledgeBase(t *testing.T) {
	service, err := NewConversationService(
		&fakeConversationRepository{err: domain.ErrNotFound},
		&sequentialIDGenerator{},
		fixedClock{now: time.Now()},
	)
	if err != nil {
		t.Fatalf("NewConversationService: %v", err)
	}
	_, err = service.Create(context.Background(), CreateConversationRequest{
		CustomerID:      "customer-1",
		KnowledgeBaseID: "missing-base",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "knowledge_base_not_found" {
		t.Fatalf("unexpected failure: %v", err)
	}
}

func TestReadEventsUsesScopedPagination(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeEventRepository{
		page: domain.RunEventPage{
			RunID:  "run-1",
			Status: domain.RunStatusRunning,
			Events: []domain.RunEvent{
				{
					ID:        "event-2",
					RunID:     "run-1",
					Sequence:  2,
					Type:      domain.EventTypeRunStarted,
					Payload:   map[string]any{"attempt": 1},
					CreatedAt: now,
				},
			},
		},
	}
	service, err := NewEventService(repository)
	if err != nil {
		t.Fatalf("NewEventService: %v", err)
	}
	page, err := service.ReadEvents(context.Background(), EventRequest{
		CustomerID:    " customer-1 ",
		RunID:         " run-1 ",
		AfterSequence: 1,
	})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if page.Events[0].Sequence != 2 ||
		repository.customerID != "customer-1" ||
		repository.runID != "run-1" ||
		repository.afterSequence != 1 ||
		repository.limit != 100 {
		t.Fatalf("unexpected event read: page=%#v repository=%#v", page, repository)
	}
}

func TestReadEventsRejectsNonMonotonicResult(t *testing.T) {
	repository := &fakeEventRepository{
		page: domain.RunEventPage{
			RunID:  "run-1",
			Status: domain.RunStatusRunning,
			Events: []domain.RunEvent{
				{
					ID:        "event-1",
					RunID:     "run-1",
					Sequence:  1,
					Type:      domain.EventTypeRunStarted,
					Payload:   map[string]any{"attempt": 1},
					CreatedAt: time.Now(),
				},
			},
		},
	}
	service, err := NewEventService(repository)
	if err != nil {
		t.Fatalf("NewEventService: %v", err)
	}
	_, err = service.ReadEvents(context.Background(), EventRequest{
		CustomerID:    "customer-1",
		RunID:         "run-1",
		AfterSequence: 1,
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "invalid_run_event_result" {
		t.Fatalf("unexpected failure: %v", err)
	}
}
