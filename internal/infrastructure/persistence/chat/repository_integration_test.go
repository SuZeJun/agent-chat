package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"
	"agent-chat/internal/infrastructure/persistence"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSendMessageLifecycleAgainstPostgres(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-chat")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-1",
		CustomerID:      "customer-1",
		KnowledgeBaseID: "base-chat",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	service := newChatService(t, repository)
	request := application.Request{
		RequestID:       "request-idempotency",
		CustomerID:      "customer-1",
		ConversationID:  "conversation-1",
		ClientMessageID: "client-message-1",
		Content:         "如何重置密码？",
	}

	first, err := service.SendMessage(ctx, request)
	if err != nil {
		t.Fatalf("send first message: %v", err)
	}
	if first.Duplicate || first.RunStatus != domain.RunStatusPending {
		t.Fatalf("unexpected first result: %#v", first)
	}
	assertStartedRows(t, ctx, pool, request, first)

	replayed, err := service.SendMessage(ctx, request)
	if err != nil {
		t.Fatalf("replay message: %v", err)
	}
	if !replayed.Duplicate ||
		replayed.MessageID != first.MessageID ||
		replayed.RunID != first.RunID {
		t.Fatalf("unexpected replay result: %#v", replayed)
	}
	assertStartedCounts(t, ctx, pool, "conversation-1", 1, 1, 1, 1)

	conflicting := request
	conflicting.Content = "同一个客户端 ID 的不同内容"
	_, err = service.SendMessage(ctx, conflicting)
	assertApplicationFailure(t, err, "client_message_id_conflict")
	assertStartedCounts(t, ctx, pool, "conversation-1", 1, 1, 1, 1)

	if _, err := pool.Exec(ctx, `
		UPDATE conversations
		SET status = 'waiting_human'
		WHERE id = 'conversation-1'
	`); err != nil {
		t.Fatalf("mark conversation waiting human: %v", err)
	}
	newRequest := request
	newRequest.ClientMessageID = "client-message-2"
	_, err = service.SendMessage(ctx, newRequest)
	assertApplicationFailure(t, err, "conversation_not_ai_active")

	unauthorized := newRequest
	unauthorized.CustomerID = "customer-2"
	_, err = service.SendMessage(ctx, unauthorized)
	assertApplicationFailure(t, err, "conversation_not_found")
}

func TestStartRunRollsBackWhenJobInsertConflicts(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-rollback")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-rollback",
		CustomerID:      "customer-rollback",
		KnowledgeBaseID: "base-rollback",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (id, job_type, status)
		VALUES ('job-conflict', 'test.conflict', 'pending')
	`); err != nil {
		t.Fatalf("seed conflicting job: %v", err)
	}

	submission := testStartSubmission(
		"conversation-rollback",
		"customer-rollback",
		"rollback",
		now,
	)
	submission.JobID = "job-conflict"
	_, err := repository.StartRun(ctx, submission)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected job conflict, got %v", err)
	}

	assertStartedCounts(t, ctx, pool, "conversation-rollback", 0, 0, 0, 0)
	var lastMessageAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT last_message_at
		FROM conversations
		WHERE id = 'conversation-rollback'
	`).Scan(&lastMessageAt); err != nil {
		t.Fatalf("load rolled back conversation: %v", err)
	}
	if lastMessageAt != nil {
		t.Fatalf("conversation activity changed after rollback: %v", lastMessageAt)
	}
}

func TestConcurrentClientMessageRetriesCreateOneRun(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-concurrent")
	createConversation(t, ctx, repository, domain.Conversation{
		ID:              "conversation-concurrent",
		CustomerID:      "customer-concurrent",
		KnowledgeBaseID: "base-concurrent",
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	service := newChatService(t, repository)
	request := application.Request{
		RequestID:       "request-concurrent",
		CustomerID:      "customer-concurrent",
		ConversationID:  "conversation-concurrent",
		ClientMessageID: "client-message-concurrent",
		Content:         "并发重试问题",
	}

	const goroutines = 12
	start := make(chan struct{})
	results := make(chan application.Result, goroutines)
	failures := make(chan error, goroutines)
	var waitGroup sync.WaitGroup
	for index := 0; index < goroutines; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			result, err := service.SendMessage(ctx, request)
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	close(failures)

	for err := range failures {
		t.Fatalf("concurrent send failed: %v", err)
	}
	var messageID string
	var runID string
	created := 0
	count := 0
	for result := range results {
		count++
		if messageID == "" {
			messageID = result.MessageID
			runID = result.RunID
		}
		if result.MessageID != messageID || result.RunID != runID {
			t.Fatalf("concurrent retries returned different entities: %#v", result)
		}
		if !result.Duplicate {
			created++
		}
	}
	if count != goroutines || created != 1 {
		t.Fatalf("unexpected concurrent results: count=%d created=%d", count, created)
	}
	assertStartedCounts(t, ctx, pool, "conversation-concurrent", 1, 1, 1, 1)
}

func openChatTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	schemaName := fmt.Sprintf("chat_repository_test_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(cleanupContext, "DROP SCHEMA "+schemaIdentifier+" CASCADE")
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := persistence.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

func newChatService(t *testing.T, repository domain.Repository) *application.Service {
	t.Helper()
	service, err := application.NewService(
		repository,
		application.UUIDGenerator{},
		application.SystemClock{},
	)
	if err != nil {
		t.Fatalf("create chat service: %v", err)
	}
	return service
}

func createKnowledgeBase(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	id string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO knowledge_bases (id, name, status)
		VALUES ($1, $2, 'active')
	`, id, "聊天测试知识库 "+id); err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
}

func createConversation(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	conversation domain.Conversation,
) {
	t.Helper()
	if err := repository.CreateConversation(ctx, conversation); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
}

func assertStartedRows(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	request application.Request,
	result application.Result,
) {
	t.Helper()

	var messageContent string
	if err := pool.QueryRow(ctx, `
		SELECT content
		FROM messages
		WHERE id = $1
		  AND conversation_id = $2
		  AND client_message_id = $3
		  AND role = 'customer'
	`, result.MessageID, request.ConversationID, request.ClientMessageID).Scan(
		&messageContent,
	); err != nil {
		t.Fatalf("load customer message: %v", err)
	}
	if messageContent != request.Content {
		t.Fatalf("unexpected persisted message: %q", messageContent)
	}

	var runStatus string
	if err := pool.QueryRow(ctx, `
		SELECT status
		FROM agent_runs
		WHERE id = $1
		  AND source_message_id = $2
	`, result.RunID, result.MessageID).Scan(&runStatus); err != nil {
		t.Fatalf("load agent run: %v", err)
	}
	if runStatus != string(domain.RunStatusPending) {
		t.Fatalf("unexpected run status: %s", runStatus)
	}

	var eventType string
	var sequence int
	var rawEventPayload []byte
	if err := pool.QueryRow(ctx, `
		SELECT event_type, sequence, payload
		FROM run_events
		WHERE run_id = $1
	`, result.RunID).Scan(&eventType, &sequence, &rawEventPayload); err != nil {
		t.Fatalf("load initial run event: %v", err)
	}
	var eventPayload map[string]any
	if err := json.Unmarshal(rawEventPayload, &eventPayload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	if eventType != string(domain.EventTypeRunStatus) ||
		sequence != 1 ||
		eventPayload["status"] != string(domain.RunStatusPending) {
		t.Fatalf(
			"unexpected initial event: type=%s sequence=%d payload=%#v",
			eventType,
			sequence,
			eventPayload,
		)
	}

	var jobStatus string
	var idempotencyKey string
	var rawJobPayload []byte
	if err := pool.QueryRow(ctx, `
		SELECT status, idempotency_key, payload
		FROM jobs
		WHERE job_type = $1
		  AND idempotency_key = $2
	`, domain.AgentRunJobType, result.RunID).Scan(
		&jobStatus,
		&idempotencyKey,
		&rawJobPayload,
	); err != nil {
		t.Fatalf("load agent run job: %v", err)
	}
	var jobPayload map[string]string
	if err := json.Unmarshal(rawJobPayload, &jobPayload); err != nil {
		t.Fatalf("decode job payload: %v", err)
	}
	if jobStatus != "pending" ||
		idempotencyKey != result.RunID ||
		jobPayload["run_id"] != result.RunID {
		t.Fatalf(
			"unexpected job: status=%s idempotency=%s payload=%#v",
			jobStatus,
			idempotencyKey,
			jobPayload,
		)
	}
}

func assertStartedCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	conversationID string,
	messages int,
	runs int,
	events int,
	jobs int,
) {
	t.Helper()

	queries := []struct {
		name     string
		query    string
		expected int
	}{
		{
			name:     "messages",
			query:    `SELECT count(*) FROM messages WHERE conversation_id = $1`,
			expected: messages,
		},
		{
			name:     "runs",
			query:    `SELECT count(*) FROM agent_runs WHERE conversation_id = $1`,
			expected: runs,
		},
		{
			name: "events",
			query: `
				SELECT count(*)
				FROM run_events AS event
				JOIN agent_runs AS run ON run.id = event.run_id
				WHERE run.conversation_id = $1
			`,
			expected: events,
		},
		{
			name: "jobs",
			query: `
				SELECT count(*)
				FROM jobs AS job
				JOIN agent_runs AS run ON run.id = job.idempotency_key
				WHERE run.conversation_id = $1
				  AND job.job_type = 'agent.run'
			`,
			expected: jobs,
		},
	}
	for _, assertion := range queries {
		var actual int
		if err := pool.QueryRow(ctx, assertion.query, conversationID).Scan(&actual); err != nil {
			t.Fatalf("count %s: %v", assertion.name, err)
		}
		if actual != assertion.expected {
			t.Fatalf(
				"unexpected %s count: got=%d want=%d",
				assertion.name,
				actual,
				assertion.expected,
			)
		}
	}
}

func testStartSubmission(
	conversationID string,
	customerID string,
	suffix string,
	now time.Time,
) domain.StartRunSubmission {
	messageID := "message-" + suffix
	runID := "run-" + suffix
	return domain.StartRunSubmission{
		CustomerID: customerID,
		Message: domain.Message{
			ID:              messageID,
			ConversationID:  conversationID,
			ClientMessageID: "client-" + suffix,
			Role:            domain.MessageRoleCustomer,
			Content:         "测试问题 " + suffix,
			CreatedAt:       now,
		},
		Run: domain.AgentRun{
			ID:              runID,
			RequestID:       "request-" + runID,
			ConversationID:  conversationID,
			SourceMessageID: messageID,
			Status:          domain.RunStatusPending,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		Event: domain.RunEvent{
			ID:        "event-" + suffix,
			RunID:     runID,
			Sequence:  1,
			Type:      domain.EventTypeRunStatus,
			Payload:   map[string]any{"status": string(domain.RunStatusPending)},
			CreatedAt: now,
		},
		JobID: "job-" + suffix,
	}
}

func assertApplicationFailure(t *testing.T, err error, code string) {
	t.Helper()
	var failure *application.Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("unexpected application failure: %v", err)
	}
}
