package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunExecutionLifecycleAgainstPostgres(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-execution")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-execution",
		CustomerID:      "customer-execution",
		KnowledgeBaseID: "base-execution",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	service := newChatService(t, repository)
	started, err := service.SendMessage(ctx, application.Request{
		RequestID:       "request-execution",
		CustomerID:      "customer-execution",
		ConversationID:  "conversation-execution",
		ClientMessageID: "client-execution",
		Content:         "如何重置密码？",
	})
	if err != nil {
		t.Fatalf("create pending run: %v", err)
	}

	source, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   started.RunID,
		Attempt: 1,
		Event:   executionEvent("event-started", domain.EventTypeRunStarted, now.Add(time.Second), map[string]any{"attempt": 1}),
	})
	if err != nil {
		t.Fatalf("begin run attempt: %v", err)
	}
	if source.Run.Status != domain.RunStatusRunning ||
		source.Message.Content != "如何重置密码？" ||
		source.KnowledgeBaseID != "base-execution" {
		t.Fatalf("unexpected run source: %#v", source)
	}

	completedAt := now.Add(2 * time.Second)
	command := domain.CompleteRunCommand{
		RunID: started.RunID,
		Message: domain.Message{
			ID:             "message-assistant",
			ConversationID: "conversation-execution",
			AgentRunID:     started.RunID,
			Role:           domain.MessageRoleAssistant,
			Content:        "请在设置页重置密码。[S1]",
			CreatedAt:      completedAt,
		},
		Result: map[string]any{
			"answer":   "请在设置页重置密码。[S1]",
			"nodePath": []string{"retrieve", "answerability", "generate"},
		},
		Events: []domain.EventDraft{
			executionEvent("event-retrieval", domain.EventTypeRetrievalCompleted, completedAt, map[string]any{"evidence": []any{}}),
			executionEvent("event-answerability", domain.EventTypeAnswerabilityDecided, completedAt, map[string]any{"decision": "answerable"}),
			executionEvent("event-delta", domain.EventTypeMessageDelta, completedAt, map[string]any{"delta": "请在设置页重置密码。[S1]"}),
			executionEvent("event-citation", domain.EventTypeMessageCitation, completedAt, map[string]any{"sourceId": "S1"}),
			executionEvent("event-completed", domain.EventTypeRunCompleted, completedAt, map[string]any{"status": "completed"}),
		},
		Steps: []domain.RunStepDraft{
			{
				Name:             "grounded_generate",
				Component:        "ChatModel",
				ComponentType:    "deepseek/deepseek-v4-flash",
				Status:           "completed",
				StartedAt:        completedAt.Add(-250 * time.Millisecond),
				CompletedAt:      completedAt,
				DurationMillis:   250,
				PromptTokens:     120,
				CompletionTokens: 30,
			},
		},
		CompletedAt: completedAt,
	}
	if err := repository.CompleteRun(ctx, command); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	assertCompletedRun(t, ctx, pool, started.RunID, command)
	trace, err := repository.LoadRunTrace(ctx, started.RunID)
	if err != nil {
		t.Fatalf("load run Trace: %v", err)
	}
	if trace.RequestID != "request-execution" ||
		trace.ConversationID != "conversation-execution" ||
		len(trace.Steps) != 1 ||
		trace.Steps[0].Name != "grounded_generate" ||
		trace.Steps[0].PromptTokens != 120 ||
		trace.Result["answer"] != command.Result["answer"] {
		t.Fatalf("unexpected persisted Trace: %#v", trace)
	}

	replayed, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   started.RunID,
		Attempt: 2,
		Event:   executionEvent("event-replayed", domain.EventTypeRunStarted, completedAt.Add(time.Second), map[string]any{"attempt": 2}),
	})
	if err != nil {
		t.Fatalf("replay completed run: %v", err)
	}
	if !replayed.Terminal() || replayed.Run.Status != domain.RunStatusCompleted {
		t.Fatalf("completed run was not terminal: %#v", replayed)
	}
	if err := repository.CompleteRun(ctx, command); err != nil {
		t.Fatalf("idempotent completion failed: %v", err)
	}
	assertRunEventCount(t, ctx, pool, started.RunID, 7)
}

func TestRunFailureRetriesAndTerminatesAgainstPostgres(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-failure")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-failure",
		CustomerID:      "customer-failure",
		KnowledgeBaseID: "base-failure",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	service := newChatService(t, repository)
	started, err := service.SendMessage(ctx, application.Request{
		RequestID:       "request-failure",
		CustomerID:      "customer-failure",
		ConversationID:  "conversation-failure",
		ClientMessageID: "client-failure",
		Content:         "会失败的问题",
	})
	if err != nil {
		t.Fatalf("create pending run: %v", err)
	}

	if _, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   started.RunID,
		Attempt: 1,
		Event:   executionEvent("event-start-1", domain.EventTypeRunStarted, now.Add(time.Second), map[string]any{"attempt": 1}),
	}); err != nil {
		t.Fatalf("begin first attempt: %v", err)
	}
	retryAt := now.Add(2 * time.Second)
	if err := repository.RecordRunFailure(ctx, domain.RecordRunFailureCommand{
		RunID:     started.RunID,
		Attempt:   1,
		ErrorCode: "rag_generation_failed",
		Terminal:  false,
		Event: executionEvent("event-retry", domain.EventTypeRunStatus, retryAt, map[string]any{
			"status":    "running",
			"retrying":  true,
			"errorCode": "rag_generation_failed",
		}),
		OccurredAt: retryAt,
	}); err != nil {
		t.Fatalf("record retryable failure: %v", err)
	}
	assertRunStatus(t, ctx, pool, started.RunID, "running", "")

	if _, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   started.RunID,
		Attempt: 2,
		Event:   executionEvent("event-start-2", domain.EventTypeRunStarted, now.Add(3*time.Second), map[string]any{"attempt": 2}),
	}); err != nil {
		t.Fatalf("begin second attempt: %v", err)
	}
	failedAt := now.Add(4 * time.Second)
	failure := domain.RecordRunFailureCommand{
		RunID:     started.RunID,
		Attempt:   2,
		ErrorCode: "rag_generation_failed",
		Terminal:  true,
		Event: executionEvent("event-failed", domain.EventTypeRunFailed, failedAt, map[string]any{
			"status":    "failed",
			"retrying":  false,
			"errorCode": "rag_generation_failed",
		}),
		OccurredAt: failedAt,
	}
	if err := repository.RecordRunFailure(ctx, failure); err != nil {
		t.Fatalf("record terminal failure: %v", err)
	}
	assertRunStatus(t, ctx, pool, started.RunID, "failed", "rag_generation_failed")
	assertRunEventCount(t, ctx, pool, started.RunID, 5)

	if err := repository.RecordRunFailure(ctx, failure); err != nil {
		t.Fatalf("idempotent terminal failure failed: %v", err)
	}
	replayed, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   started.RunID,
		Attempt: 3,
		Event:   executionEvent("event-start-3", domain.EventTypeRunStarted, now.Add(5*time.Second), map[string]any{"attempt": 3}),
	})
	if err != nil {
		t.Fatalf("replay failed run: %v", err)
	}
	if !replayed.Terminal() || replayed.Run.Status != domain.RunStatusFailed {
		t.Fatalf("failed run was not terminal: %#v", replayed)
	}
	assertRunEventCount(t, ctx, pool, started.RunID, 5)
}

func TestCompleteRunRejectsConversationTakeover(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-takeover")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-takeover",
		CustomerID:      "customer-takeover",
		KnowledgeBaseID: "base-takeover",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	service := newChatService(t, repository)
	started, err := service.SendMessage(ctx, application.Request{
		RequestID:       "request-takeover",
		CustomerID:      "customer-takeover",
		ConversationID:  "conversation-takeover",
		ClientMessageID: "client-takeover",
		Content:         "接管期间的问题",
	})
	if err != nil {
		t.Fatalf("create pending run: %v", err)
	}
	if _, err := repository.BeginRunAttempt(ctx, domain.BeginRunAttempt{
		RunID:   started.RunID,
		Attempt: 1,
		Event:   executionEvent("event-takeover-start", domain.EventTypeRunStarted, now.Add(time.Second), map[string]any{"attempt": 1}),
	}); err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE conversations
		SET status = 'human_active'
		WHERE id = 'conversation-takeover'
	`); err != nil {
		t.Fatalf("take over conversation: %v", err)
	}

	command := domain.CompleteRunCommand{
		RunID: started.RunID,
		Message: domain.Message{
			ID:             "message-takeover-assistant",
			ConversationID: "conversation-takeover",
			AgentRunID:     started.RunID,
			Role:           domain.MessageRoleAssistant,
			Content:        "不应写入的回答",
			CreatedAt:      now.Add(2 * time.Second),
		},
		Result: map[string]any{"answer": "不应写入的回答"},
		Events: []domain.EventDraft{
			executionEvent("event-takeover-retrieval", domain.EventTypeRetrievalCompleted, now.Add(2*time.Second), map[string]any{}),
			executionEvent("event-takeover-gate", domain.EventTypeAnswerabilityDecided, now.Add(2*time.Second), map[string]any{}),
			executionEvent("event-takeover-delta", domain.EventTypeMessageDelta, now.Add(2*time.Second), map[string]any{}),
			executionEvent("event-takeover-completed", domain.EventTypeRunCompleted, now.Add(2*time.Second), map[string]any{}),
		},
		CompletedAt: now.Add(2 * time.Second),
	}
	err = repository.CompleteRun(ctx, command)
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("expected takeover rejection, got %v", err)
	}
	var assistantCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM messages
		WHERE agent_run_id = $1
	`, started.RunID).Scan(&assistantCount); err != nil {
		t.Fatalf("count assistant messages: %v", err)
	}
	if assistantCount != 0 {
		t.Fatal("assistant response persisted after takeover")
	}
	assertRunStatus(t, ctx, pool, started.RunID, "running", "")
	assertRunEventCount(t, ctx, pool, started.RunID, 2)
}

func executionEvent(
	id string,
	eventType domain.EventType,
	createdAt time.Time,
	payload map[string]any,
) domain.EventDraft {
	return domain.EventDraft{
		ID:        id,
		Type:      eventType,
		Payload:   payload,
		CreatedAt: createdAt,
	}
}

func assertCompletedRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runID string,
	command domain.CompleteRunCommand,
) {
	t.Helper()
	var status string
	var errorCode string
	var rawResult []byte
	var startedAt time.Time
	var completedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, error_code, result, started_at, completed_at
		FROM agent_runs
		WHERE id = $1
	`, runID).Scan(
		&status,
		&errorCode,
		&rawResult,
		&startedAt,
		&completedAt,
	); err != nil {
		t.Fatalf("load completed run: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(rawResult, &result); err != nil {
		t.Fatalf("decode completed result: %v", err)
	}
	if status != "completed" ||
		errorCode != "" ||
		result["answer"] != command.Result["answer"] ||
		completedAt.Sub(command.CompletedAt).Abs() > time.Microsecond ||
		startedAt.IsZero() {
		t.Fatalf(
			"unexpected completed run: status=%s error=%s result=%#v started=%v completed=%v",
			status,
			errorCode,
			result,
			startedAt,
			completedAt,
		)
	}

	var messageContent string
	if err := pool.QueryRow(ctx, `
		SELECT content
		FROM messages
		WHERE agent_run_id = $1
		  AND role = 'assistant'
	`, runID).Scan(&messageContent); err != nil {
		t.Fatalf("load assistant message: %v", err)
	}
	if messageContent != command.Message.Content {
		t.Fatalf("unexpected assistant message: %q", messageContent)
	}

	rows, err := pool.Query(ctx, `
		SELECT sequence, event_type
		FROM run_events
		WHERE run_id = $1
		ORDER BY sequence
	`, runID)
	if err != nil {
		t.Fatalf("load run events: %v", err)
	}
	defer rows.Close()
	var sequences []int
	var eventTypes []string
	for rows.Next() {
		var sequence int
		var eventType string
		if err := rows.Scan(&sequence, &eventType); err != nil {
			t.Fatalf("scan run event: %v", err)
		}
		sequences = append(sequences, sequence)
		eventTypes = append(eventTypes, eventType)
	}
	expectedTypes := []string{
		"run.status",
		"run.started",
		"retrieval.completed",
		"answerability.decided",
		"message.delta",
		"message.citation",
		"run.completed",
	}
	if !reflect.DeepEqual(sequences, []int{1, 2, 3, 4, 5, 6, 7}) ||
		!reflect.DeepEqual(eventTypes, expectedTypes) {
		t.Fatalf("unexpected persisted events: seq=%v types=%v", sequences, eventTypes)
	}
}

func assertRunStatus(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runID string,
	expectedStatus string,
	expectedError string,
) {
	t.Helper()
	var status string
	var errorCode string
	if err := pool.QueryRow(ctx, `
		SELECT status, error_code
		FROM agent_runs
		WHERE id = $1
	`, runID).Scan(&status, &errorCode); err != nil {
		t.Fatalf("load run status: %v", err)
	}
	if status != expectedStatus || errorCode != expectedError {
		t.Fatalf("unexpected run state: status=%s error=%s", status, errorCode)
	}
}

func assertRunEventCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runID string,
	expected int,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM run_events
		WHERE run_id = $1
	`, runID).Scan(&count); err != nil {
		t.Fatalf("count run events: %v", err)
	}
	if count != expected {
		t.Fatalf("unexpected run event count: got=%d want=%d", count, expected)
	}
}
