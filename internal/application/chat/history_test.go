package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "agent-chat/internal/domain/chat"
)

type fakeHistoryRepository struct {
	query domain.MessageHistoryQuery
	page  domain.MessageHistoryPage
	err   error
}

func (repository *fakeHistoryRepository) LoadMessageHistory(
	_ context.Context,
	query domain.MessageHistoryQuery,
) (domain.MessageHistoryPage, error) {
	repository.query = query
	return repository.page, repository.err
}

func TestReadMessageHistoryUsesCustomerScopeAndDefaultPage(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeHistoryRepository{page: domain.MessageHistoryPage{
		Items: []domain.MessageHistoryItem{
			{
				Message: domain.Message{
					ID:              "message-1",
					ConversationID:  "conversation-1",
					ClientMessageID: "client-1",
					Role:            domain.MessageRoleCustomer,
					Content:         "无法导出账单",
					CreatedAt:       now,
				},
				RunID:     "run-1",
				RunStatus: domain.RunStatusPending,
			},
		},
	}}
	service, err := NewHistoryService(repository)
	if err != nil {
		t.Fatalf("NewHistoryService: %v", err)
	}

	page, err := service.ReadMessageHistory(context.Background(), MessageHistoryRequest{
		CustomerID:      " customer-1 ",
		ConversationID:  " conversation-1 ",
		BeforeMessageID: " message-2 ",
	})
	if err != nil {
		t.Fatalf("ReadMessageHistory: %v", err)
	}
	if len(page.Items) != 1 ||
		repository.query.CustomerID != "customer-1" ||
		repository.query.ConversationID != "conversation-1" ||
		repository.query.BeforeMessageID != "message-2" ||
		repository.query.Limit != defaultMessageHistoryPageSize {
		t.Fatalf("unexpected history read: query=%#v page=%#v", repository.query, page)
	}
}

func TestReadMessageHistoryMapsCustomerIsolationToNotFound(t *testing.T) {
	service, err := NewHistoryService(&fakeHistoryRepository{err: domain.ErrNotFound})
	if err != nil {
		t.Fatalf("NewHistoryService: %v", err)
	}
	_, err = service.ReadMessageHistory(context.Background(), MessageHistoryRequest{
		CustomerID:     "other-customer",
		ConversationID: "conversation-1",
		Limit:          20,
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "conversation_not_found" {
		t.Fatalf("unexpected failure: %v", err)
	}
}

func TestReadMessageHistoryRejectsOutOfOrderRepositoryResult(t *testing.T) {
	now := time.Now().UTC()
	items := make([]domain.MessageHistoryItem, 0, 2)
	for index, createdAt := range []time.Time{now, now.Add(-time.Second)} {
		items = append(items, domain.MessageHistoryItem{
			Message: domain.Message{
				ID:              "message-" + string(rune('1'+index)),
				ConversationID:  "conversation-1",
				ClientMessageID: "client-" + string(rune('1'+index)),
				Role:            domain.MessageRoleCustomer,
				Content:         "问题",
				CreatedAt:       createdAt,
			},
			RunID:     "run-" + string(rune('1'+index)),
			RunStatus: domain.RunStatusPending,
		})
	}
	service, err := NewHistoryService(&fakeHistoryRepository{
		page: domain.MessageHistoryPage{Items: items},
	})
	if err != nil {
		t.Fatalf("NewHistoryService: %v", err)
	}
	_, err = service.ReadMessageHistory(context.Background(), MessageHistoryRequest{
		CustomerID:     "customer-1",
		ConversationID: "conversation-1",
		Limit:          20,
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "invalid_message_history_result" {
		t.Fatalf("unexpected failure: %v", err)
	}
}
