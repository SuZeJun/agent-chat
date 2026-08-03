package chatpg

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	domain "agent-chat/internal/domain/chat"
)

func TestMessageHistoryAndPlanningSnapshotAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openChatTestDatabase(t, ctx, databaseURL)
	defer pool.Close()
	repository := NewRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	createKnowledgeBase(t, ctx, pool, "base-history")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-history",
		CustomerID:      "customer-history",
		KnowledgeBaseID: "base-history",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})

	first := testStartSubmission("conversation-history", "customer-history", "history-1", now.Add(time.Second))
	first.Message.Content = "账单导出按钮没有反应"
	startAndCompleteHistoryRun(t, ctx, repository, first, "请更换浏览器后再试", map[string]any{
		"answer": "请更换浏览器后再试",
		"assessment": map[string]any{
			"decision": "answerable",
		},
		"citations": []any{map[string]any{"sourceId": "S1"}},
	}, now.Add(2*time.Second), now.Add(3*time.Second))

	second := testStartSubmission("conversation-history", "customer-history", "history-2", now.Add(4*time.Second))
	second.Message.Content = "Chrome 和 Edge 都试过了，页面没有错误提示"
	startAndCompleteHistoryRun(t, ctx, repository, second, "请确认影响范围", map[string]any{
		"answer": "请确认影响范围",
		"assessment": map[string]any{
			"decision": "needs_clarification",
		},
	}, now.Add(5*time.Second), now.Add(6*time.Second))

	third := testStartSubmission("conversation-history", "customer-history", "history-3", now.Add(7*time.Second))
	third.Message.Content = "所有账单都不行，帮我建个工单"
	if _, err := repository.StartRun(ctx, third); err != nil {
		t.Fatalf("start third run: %v", err)
	}
	source, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:        third.Run.ID,
		Attempt:      1,
		HistoryLimit: 3,
		Event: executionEvent(
			"event-history-third-started",
			domain.EventTypeRunStarted,
			now.Add(8*time.Second),
			map[string]any{"attempt": 1},
		),
	})
	if err != nil {
		t.Fatalf("begin third run: %v", err)
	}
	wantHistory := []string{
		"请更换浏览器后再试",
		"Chrome 和 Edge 都试过了，页面没有错误提示",
		"请确认影响范围",
	}
	if got := historyContents(source.History); !reflect.DeepEqual(got, wantHistory) {
		t.Fatalf("planning history = %#v, want %#v", got, wantHistory)
	}

	// 即使重试前出现了更晚消息，规划快照也只能读取源消息之前的同会话数据。
	if _, err := pool.Exec(ctx, `
		INSERT INTO messages (id, conversation_id, role, content, created_at)
		VALUES ('message-history-future', 'conversation-history', 'system', '更晚的系统消息', $1)
	`, now.Add(9*time.Second)); err != nil {
		t.Fatalf("insert later message: %v", err)
	}
	replayed, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:        third.Run.ID,
		Attempt:      2,
		HistoryLimit: 3,
		Event: executionEvent(
			"event-history-third-retry",
			domain.EventTypeRunStarted,
			now.Add(10*time.Second),
			map[string]any{"attempt": 2},
		),
	})
	if err != nil {
		t.Fatalf("retry third run: %v", err)
	}
	if got := historyContents(replayed.History); !reflect.DeepEqual(got, wantHistory) {
		t.Fatalf("retry planning history changed: %#v", got)
	}

	recent, err := repository.LoadMessageHistory(ctx, domain.MessageHistoryQuery{
		CustomerID:     "customer-history",
		ConversationID: "conversation-history",
		Limit:          2,
	})
	if err != nil {
		t.Fatalf("load recent history: %v", err)
	}
	if len(recent.Items) != 2 || recent.NextBeforeMessageID == "" {
		t.Fatalf("unexpected recent page: %#v", recent)
	}
	if recent.Items[0].Message.ID != third.Message.ID ||
		recent.Items[0].RunID != third.Run.ID ||
		recent.Items[0].RunStatus != domain.RunStatusRunning ||
		recent.Items[1].Message.ID != "message-history-future" {
		t.Fatalf("recent page lost Run state or ordering: %#v", recent.Items)
	}

	older, err := repository.LoadMessageHistory(ctx, domain.MessageHistoryQuery{
		CustomerID:      "customer-history",
		ConversationID:  "conversation-history",
		BeforeMessageID: recent.NextBeforeMessageID,
		Limit:           2,
	})
	if err != nil {
		t.Fatalf("load older history: %v", err)
	}
	if len(older.Items) != 2 || older.Items[0].Message.ID != second.Message.ID {
		t.Fatalf("unexpected older page: %#v", older)
	}
	if older.Items[1].Message.Role != domain.MessageRoleAssistant ||
		older.Items[1].RunResult["assessment"] == nil {
		t.Fatalf("assistant Result was not restored: %#v", older.Items[1])
	}

	_, err = repository.LoadMessageHistory(ctx, domain.MessageHistoryQuery{
		CustomerID:     "another-customer",
		ConversationID: "conversation-history",
		Limit:          20,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-customer history read returned %v", err)
	}
}

func startAndCompleteHistoryRun(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	submission domain.StartRunSubmission,
	answer string,
	result map[string]any,
	startedAt time.Time,
	completedAt time.Time,
) {
	t.Helper()
	if _, err := repository.StartRun(ctx, submission); err != nil {
		t.Fatalf("start %s: %v", submission.Run.ID, err)
	}
	if _, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   submission.Run.ID,
		Attempt: 1,
		Event: executionEvent(
			"event-started-"+submission.Run.ID,
			domain.EventTypeRunStarted,
			startedAt,
			map[string]any{"attempt": 1},
		),
	}); err != nil {
		t.Fatalf("begin %s: %v", submission.Run.ID, err)
	}
	if err := repository.CompleteRun(ctx, domain.CompleteRunCommand{
		RunID: submission.Run.ID,
		Message: domain.Message{
			ID:             "assistant-" + submission.Run.ID,
			ConversationID: submission.Run.ConversationID,
			AgentRunID:     submission.Run.ID,
			Role:           domain.MessageRoleAssistant,
			Content:        answer,
			CreatedAt:      completedAt,
		},
		Result: result,
		Events: []domain.EventDraft{
			executionEvent(
				"event-completed-"+submission.Run.ID,
				domain.EventTypeRunCompleted,
				completedAt,
				map[string]any{"status": "completed"},
			),
		},
		CompletedAt: completedAt,
	}); err != nil {
		t.Fatalf("complete %s: %v", submission.Run.ID, err)
	}
}

func historyContents(messages []domain.Message) []string {
	contents := make([]string, len(messages))
	for index, message := range messages {
		contents[index] = message.Content
	}
	return contents
}
