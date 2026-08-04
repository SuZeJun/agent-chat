package chatpg

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHandoffLifecycleAgainstPostgres(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-handoff")
	createConversation(t, ctx, repository, domain.Conversation{
		ID: "conversation-handoff", CustomerID: "customer-handoff", KnowledgeBaseID: "base-handoff",
		Status: domain.ConversationStatusAIActive, CreatedAt: now, UpdatedAt: now,
	})
	if _, err := newChatService(t, repository).SendMessage(ctx, application.Request{
		RequestID: "request-before-handoff", CustomerID: "customer-handoff", ConversationID: "conversation-handoff",
		ClientMessageID: "client-before-handoff", Content: "账单导出失败，情况很紧急，请转人工。",
	}); err != nil {
		t.Fatalf("seed customer message: %v", err)
	}

	handoff, err := repository.RequestHandoff(ctx, application.RequestHandoffCommand{
		CustomerID: "customer-handoff", ConversationID: "conversation-handoff", Reason: "请人工协助账单导出",
		EventID: "cevt-handoff-requested", SystemMessageID: "message-handoff-requested", OccurredAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("request handoff: %v", err)
	}
	if handoff.Conversation.Status != domain.ConversationStatusWaitingHuman ||
		handoff.Summary.CustomerRequest != "请人工协助账单导出" || len(handoff.Events) != 1 {
		t.Fatalf("unexpected waiting handoff: %#v", handoff)
	}
	// 同一客户重复请求必须只读现有接管上下文，不追加消息或事件。
	replayed, err := repository.RequestHandoff(ctx, application.RequestHandoffCommand{
		CustomerID: "customer-handoff", ConversationID: "conversation-handoff", Reason: "重复请求",
		EventID: "cevt-handoff-replay", SystemMessageID: "message-handoff-replay", OccurredAt: now.Add(2 * time.Second),
	})
	if err != nil || len(replayed.Events) != 1 {
		t.Fatalf("idempotent handoff replay: result=%#v err=%v", replayed, err)
	}

	_, err = newChatService(t, repository).SendMessage(ctx, application.Request{
		RequestID: "request-blocked", CustomerID: "customer-handoff", ConversationID: "conversation-handoff",
		ClientMessageID: "client-blocked", Content: "这条消息不能启动 AI",
	})
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("AI run was not blocked while waiting for human: %v", err)
	}
	message, duplicate, err := repository.SaveHandoffCustomerMessage(ctx, application.CustomerHandoffMessageCommand{
		CustomerID: "customer-handoff",
		Message: domain.Message{
			ID: "message-customer-handoff", ConversationID: "conversation-handoff", ClientMessageID: "client-human-1",
			Role: domain.MessageRoleCustomer, Content: "补充：导出按钮点击后没有反应。", CreatedAt: now.Add(3 * time.Second),
		},
		EventID: "cevt-customer-handoff",
	})
	if err != nil || duplicate || message.ID != "message-customer-handoff" {
		t.Fatalf("save customer handoff message: message=%#v duplicate=%v err=%v", message, duplicate, err)
	}
	assertConversationRunCount(t, ctx, pool, "conversation-handoff", 1)

	winner := concurrentTakeover(t, ctx, repository, "conversation-handoff", now.Add(4*time.Second))
	if _, err := repository.LoadHandoffConversation(ctx, otherAgent(winner), "conversation-handoff"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("non-assigned agent read active conversation: %v", err)
	}
	if _, err := repository.SaveHandoffAgentMessage(ctx, application.AgentHandoffMessageCommand{
		AgentID: otherAgent(winner),
		Message: domain.Message{ID: "message-intruder", ConversationID: "conversation-handoff", Role: domain.MessageRoleAgent, Content: "越权回复", CreatedAt: now.Add(5 * time.Second)},
		EventID: "cevt-intruder",
	}); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("non-assigned agent wrote message: %v", err)
	}
	if _, err := repository.ResumeAI(ctx, application.ResumeAICommand{
		AgentID: otherAgent(winner), ConversationID: "conversation-handoff", EventID: "cevt-intruder-resume",
		SystemMessageID: "message-intruder-resume", OccurredAt: now.Add(5 * time.Second),
	}); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("non-assigned agent resumed AI: %v", err)
	}
	if _, err := repository.SaveHandoffAgentMessage(ctx, application.AgentHandoffMessageCommand{
		AgentID: winner,
		Message: domain.Message{ID: "message-agent-reply", ConversationID: "conversation-handoff", Role: domain.MessageRoleAgent, Content: "我来协助排查导出问题。", CreatedAt: now.Add(6 * time.Second)},
		EventID: "cevt-agent-reply",
	}); err != nil {
		t.Fatalf("assigned agent reply: %v", err)
	}
	page, err := repository.LoadCustomerConversationEvents(ctx, "customer-handoff", "conversation-handoff", 0, 100)
	if err != nil {
		t.Fatalf("load customer events: %v", err)
	}
	if len(page.Events) != 4 || page.Events[0].Sequence != 1 || page.Events[3].Sequence != 4 {
		t.Fatalf("unexpected durable event sequence: %#v", page.Events)
	}

	resumed, err := repository.ResumeAI(ctx, application.ResumeAICommand{
		AgentID: winner, ConversationID: "conversation-handoff", EventID: "cevt-ai-resumed",
		SystemMessageID: "message-ai-resumed", OccurredAt: now.Add(7 * time.Second),
	})
	if err != nil {
		t.Fatalf("resume AI: %v", err)
	}
	if resumed.Conversation.Status != domain.ConversationStatusAIActive || resumed.AssignedAgentID != "" {
		t.Fatalf("unexpected resumed state: %#v", resumed)
	}
	resumedEvents, err := repository.LoadCustomerConversationEvents(ctx, "customer-handoff", "conversation-handoff", 4, 100)
	if err != nil {
		t.Fatalf("load AI resume event: %v", err)
	}
	if resumedEvents.Status != domain.ConversationStatusAIActive || len(resumedEvents.Events) != 1 ||
		resumedEvents.Events[0].Sequence != 5 || resumedEvents.Events[0].Type != domain.ConversationEventAIResumed {
		t.Fatalf("unexpected AI resume events: %#v", resumedEvents)
	}
	if _, err := newChatService(t, repository).SendMessage(ctx, application.Request{
		RequestID: "request-after-resume", CustomerID: "customer-handoff", ConversationID: "conversation-handoff",
		ClientMessageID: "client-after-resume", Content: "继续由 AI 协助",
	}); err != nil {
		t.Fatalf("AI did not resume: %v", err)
	}
	assertConversationRunCount(t, ctx, pool, "conversation-handoff", 2)
}

func TestRequestHandoffRollsBackWhenEventInsertConflicts(t *testing.T) {
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
	createKnowledgeBase(t, ctx, pool, "base-handoff-rollback")
	for _, id := range []string{"conversation-event-owner", "conversation-rollback"} {
		createConversation(t, ctx, repository, domain.Conversation{
			ID: id, CustomerID: "customer-rollback", KnowledgeBaseID: "base-handoff-rollback",
			Status: domain.ConversationStatusAIActive, CreatedAt: now, UpdatedAt: now,
		})
	}
	if _, err := repository.RequestHandoff(ctx, application.RequestHandoffCommand{
		CustomerID: "customer-rollback", ConversationID: "conversation-event-owner", EventID: "cevt-global-conflict",
		SystemMessageID: "message-event-owner", OccurredAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed conflicting event: %v", err)
	}
	_, err := repository.RequestHandoff(ctx, application.RequestHandoffCommand{
		CustomerID: "customer-rollback", ConversationID: "conversation-rollback", EventID: "cevt-global-conflict",
		SystemMessageID: "message-must-rollback", OccurredAt: now.Add(2 * time.Second),
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected event conflict, got %v", err)
	}
	var status domain.ConversationStatus
	var summaryCount, messageCount int
	if err := pool.QueryRow(ctx, "SELECT status FROM conversations WHERE id = $1", "conversation-rollback").Scan(&status); err != nil {
		t.Fatalf("load rolled back status: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM handoff_summaries WHERE conversation_id = $1", "conversation-rollback").Scan(&summaryCount); err != nil {
		t.Fatalf("count rolled back summary: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE conversation_id = $1", "conversation-rollback").Scan(&messageCount); err != nil {
		t.Fatalf("count rolled back messages: %v", err)
	}
	if status != domain.ConversationStatusAIActive || summaryCount != 0 || messageCount != 0 {
		t.Fatalf("handoff transaction leaked: status=%s summaries=%d messages=%d", status, summaryCount, messageCount)
	}
}

func concurrentTakeover(t *testing.T, ctx context.Context, repository *Repository, conversationID string, occurredAt time.Time) string {
	t.Helper()
	type result struct {
		agentID string
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var waitGroup sync.WaitGroup
	for index, agentID := range []string{"agent-one", "agent-two"} {
		waitGroup.Add(1)
		go func(index int, agentID string) {
			defer waitGroup.Done()
			<-start
			_, err := repository.TakeoverHandoff(ctx, application.TakeoverHandoffCommand{
				AgentID: agentID, ConversationID: conversationID,
				EventID: "cevt-takeover-" + agentID, SystemMessageID: "message-takeover-" + agentID,
				OccurredAt: occurredAt.Add(time.Duration(index) * time.Microsecond),
			})
			results <- result{agentID: agentID, err: err}
		}(index, agentID)
	}
	close(start)
	waitGroup.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			if winner != "" {
				t.Fatal("two agents took over the same conversation")
			}
			winner = result.agentID
			continue
		}
		if !errors.Is(result.err, domain.ErrInvalidState) {
			t.Fatalf("unexpected takeover error for %s: %v", result.agentID, result.err)
		}
	}
	if winner == "" {
		t.Fatal("no agent won concurrent takeover")
	}
	return winner
}

func otherAgent(agentID string) string {
	if agentID == "agent-one" {
		return "agent-two"
	}
	return "agent-one"
}

func assertConversationRunCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, conversationID string, expected int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM agent_runs WHERE conversation_id = $1", conversationID).Scan(&count); err != nil {
		t.Fatalf("count conversation runs: %v", err)
	}
	if count != expected {
		t.Fatalf("conversation run count=%d, want=%d", count, expected)
	}
}
