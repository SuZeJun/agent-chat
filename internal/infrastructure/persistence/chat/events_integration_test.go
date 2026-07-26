package chatpg

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"
)

func TestLoadRunEventsScopesAndResumesAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openChatTestDatabase(t, ctx, databaseURL)
	defer pool.Close()

	repository := NewRepository(pool)
	now := time.Now().UTC()
	createKnowledgeBase(t, ctx, pool, "base-events")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-events",
		CustomerID:      "customer-events",
		KnowledgeBaseID: "base-events",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	service := newChatService(t, repository)
	started, err := service.SendMessage(ctx, application.Request{
		RequestID:       "request-events",
		CustomerID:      "customer-events",
		ConversationID:  "conversation-events",
		ClientMessageID: "client-events",
		Content:         "事件断点续传问题",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	if _, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   started.RunID,
		Attempt: 1,
		Event: executionEvent(
			"event-events-start",
			domain.EventTypeRunStarted,
			now.Add(time.Second),
			map[string]any{"attempt": 1},
		),
	}); err != nil {
		t.Fatalf("begin run: %v", err)
	}
	completedAt := now.Add(2 * time.Second)
	if err := repository.CompleteRun(ctx, domain.CompleteRunCommand{
		RunID: started.RunID,
		Message: domain.Message{
			ID:             "message-events-assistant",
			ConversationID: "conversation-events",
			AgentRunID:     started.RunID,
			Role:           domain.MessageRoleAssistant,
			Content:        "事件回答",
			CreatedAt:      completedAt,
		},
		Result: map[string]any{"answer": "事件回答"},
		Events: []domain.EventDraft{
			executionEvent("event-events-retrieval", domain.EventTypeRetrievalCompleted, completedAt, map[string]any{"evidence": []any{}}),
			executionEvent("event-events-gate", domain.EventTypeAnswerabilityDecided, completedAt, map[string]any{"decision": "answerable"}),
			executionEvent("event-events-delta", domain.EventTypeMessageDelta, completedAt, map[string]any{"delta": "事件回答"}),
			executionEvent("event-events-completed", domain.EventTypeRunCompleted, completedAt, map[string]any{"status": "completed"}),
		},
		CompletedAt: completedAt,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	page, err := repository.LoadRunEvents(
		ctx,
		"customer-events",
		started.RunID,
		1,
		100,
	)
	if err != nil {
		t.Fatalf("load resumed events: %v", err)
	}
	if !page.Terminal() ||
		page.Status != domain.RunStatusCompleted ||
		len(page.Events) != 5 ||
		page.Events[0].Sequence != 2 ||
		page.Events[4].Sequence != 6 {
		t.Fatalf("unexpected event page: %#v", page)
	}

	_, err = repository.LoadRunEvents(
		ctx,
		"another-customer",
		started.RunID,
		0,
		100,
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected customer isolation, got %v", err)
	}
}
