package ticketpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	domain "agent-chat/internal/domain/ticket"
	"agent-chat/internal/infrastructure/persistence"
	ticketpg "agent-chat/internal/infrastructure/persistence/ticket"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestApprovalSafetyPropertiesAgainstPostgres 验证 docs/EVALUATION.md 7.3 列为
// 必须 100% 通过的四条写操作安全属性。
//
// 这些属性只有在真实数据库上才有意义：它们依赖条件更新的受影响行数、唯一约束
// 和事务隔离，测试替身无法复现其中任何一项。
func TestApprovalSafetyPropertiesAgainstPostgres(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool := openTicketTestDatabase(t, ctx, databaseURL)
	defer pool.Close()

	repository := ticketpg.NewRepository(pool)
	now := time.Now().UTC().Truncate(time.Millisecond)

	t.Run("未确认不执行写操作", func(t *testing.T) {
		approval := seedApproval(t, ctx, pool, "pending-1", now)

		// 直接尝试创建工单，绕过状态转换。
		_, err := repository.CreateTicket(ctx, newTicket("tkt-pending", approval))
		if !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("expected pending approval to reject ticket creation, got %v", err)
		}
		assertTicketCount(t, ctx, pool, approval.ID, 0)
	})

	t.Run("取消后不执行写操作", func(t *testing.T) {
		approval := seedApproval(t, ctx, pool, "cancel-1", now)

		if _, err := repository.Cancel(ctx, approval.CustomerID, approval.ID, "evt-cancel-1", now); err != nil {
			t.Fatalf("Cancel returned error: %v", err)
		}
		// 取消后确认必须失败。
		if _, err := repository.ConfirmAndEnqueue(ctx, confirmCommand(approval, "cancel", now)); !errors.Is(
			err,
			domain.ErrInvalidState,
		) {
			t.Fatalf("expected cancelled approval to reject approval, got %v", err)
		}
		// 即便直接调用创建也必须失败。
		if _, err := repository.CreateTicket(ctx, newTicket("tkt-cancel", approval)); !errors.Is(
			err,
			domain.ErrInvalidState,
		) {
			t.Fatalf("expected cancelled approval to reject ticket creation, got %v", err)
		}
		assertTicketCount(t, ctx, pool, approval.ID, 0)
		assertRunEventTypeCount(t, ctx, pool, approval.AgentRunID, "approval.cancelled", 1)
	})

	t.Run("过期确认不执行写操作", func(t *testing.T) {
		approval := seedApproval(t, ctx, pool, "expire-1", now)

		afterExpiry := approval.ExpiresAt.Add(time.Second)
		command := confirmCommand(approval, "expired", afterExpiry)
		if _, err := repository.ConfirmAndEnqueue(ctx, command); !errors.Is(err, domain.ErrExpired) {
			t.Fatalf("expected expired approval to be rejected, got %v", err)
		}
		// 过期的 pending 记录必须落为终态，不能继续保持可确认的外观。
		stored, err := repository.LoadApproval(ctx, approval.CustomerID, approval.ID)
		if err != nil {
			t.Fatalf("LoadApproval returned error: %v", err)
		}
		if stored.Status != domain.ApprovalStatusExpired {
			t.Fatalf("expired approval was not settled: %#v", stored)
		}
		assertTicketCount(t, ctx, pool, approval.ID, 0)
		assertRunEventTypeCount(t, ctx, pool, approval.AgentRunID, "approval.expired", 1)
	})

	t.Run("调用方时钟落后不能确认已过期审批", func(t *testing.T) {
		createdAt := now.Add(-2 * time.Hour)
		approval := seedApproval(t, ctx, pool, "database-expire-1", createdAt)

		// 模拟 API 实例的本地时钟严重落后：调用方给出的时间仍在有效期内，
		// PostgreSQL 当前时间已经超过 expires_at，仓储必须以数据库时间兜底。
		callerTime := createdAt.Add(5 * time.Minute)
		command := confirmCommand(approval, "database-expired", callerTime)
		if _, err := repository.ConfirmAndEnqueue(ctx, command); !errors.Is(err, domain.ErrExpired) {
			t.Fatalf("expected database clock to reject expired approval, got %v", err)
		}
		assertJobCount(t, ctx, pool, approval.ID, 0)
		assertTicketCount(t, ctx, pool, approval.ID, 0)
		assertRunEventTypeCount(t, ctx, pool, approval.AgentRunID, "approval.expired", 1)
	})

	t.Run("重复确认不产生重复副作用", func(t *testing.T) {
		approval := seedApproval(t, ctx, pool, "approve-1", now)

		command := confirmCommand(approval, "approve", now)
		first, err := repository.ConfirmAndEnqueue(ctx, command)
		if err != nil {
			t.Fatalf("first Approve returned error: %v", err)
		}
		if first.AlreadyApproved {
			t.Fatal("first approval must not report AlreadyApproved")
		}
		created, err := repository.ExecuteCreateTicket(ctx, executeCommand(command))
		if err != nil {
			t.Fatalf("CreateTicket returned error: %v", err)
		}

		// 第二次确认必须报告已确认，且不产生第二张工单。
		second, err := repository.ConfirmAndEnqueue(ctx, confirmCommand(approval, "replay", now))
		if err != nil {
			t.Fatalf("second Approve returned error: %v", err)
		}
		if !second.AlreadyApproved {
			t.Fatal("repeated approval must report AlreadyApproved")
		}
		if second.Ticket == nil || second.Ticket.ID != created.ID || second.Ticket.Number != created.Number {
			t.Fatalf("repeated confirmation did not return first ticket: %#v vs %#v", second.Ticket, created)
		}
		assertTicketCount(t, ctx, pool, approval.ID, 1)
		assertRunEventTypeCount(t, ctx, pool, approval.AgentRunID, "approval.confirmed", 1)
		assertRunEventTypeCount(t, ctx, pool, approval.AgentRunID, "ticket.created", 1)
	})
}

// TestConcurrentApprovalCreatesSingleTicket 验证并发确认下的幂等。
//
// 先读后写的实现会在此失败：多个请求可能同时读到 pending，各自认为自己是第一个。
func TestConcurrentApprovalCreatesSingleTicket(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool := openTicketTestDatabase(t, ctx, databaseURL)
	defer pool.Close()

	repository := ticketpg.NewRepository(pool)
	now := time.Now().UTC().Truncate(time.Millisecond)
	approval := seedApproval(t, ctx, pool, "concurrent-1", now)

	const goroutines = 8
	var group sync.WaitGroup
	results := make(chan error, goroutines)
	for index := range goroutines {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, err := repository.ConfirmAndEnqueue(
				ctx,
				confirmCommand(approval, fmt.Sprintf("concurrent-%d", index), now),
			)
			results <- err
		}(index)
	}
	group.Wait()
	close(results)

	for err := range results {
		if err != nil {
			t.Fatalf("concurrent approval returned error: %v", err)
		}
	}
	assertJobCount(t, ctx, pool, approval.ID, 1)
	command := loadCreateCommand(t, ctx, pool, approval.ID)
	if _, err := repository.ExecuteCreateTicket(ctx, command); err != nil {
		t.Fatalf("ExecuteCreateTicket returned error: %v", err)
	}
	if _, err := repository.ExecuteCreateTicket(ctx, command); err != nil {
		t.Fatalf("replayed ExecuteCreateTicket returned error: %v", err)
	}
	assertTicketCount(t, ctx, pool, approval.ID, 1)
	assertRunEventTypeCount(t, ctx, pool, approval.AgentRunID, "approval.confirmed", 1)
	assertRunEventTypeCount(t, ctx, pool, approval.AgentRunID, "ticket.created", 1)
}

func seedApproval(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	now time.Time,
) domain.Approval {
	t.Helper()

	conversationID := "conv-" + suffix
	runID := "run-" + suffix
	seedConversationAndRun(t, ctx, pool, conversationID, runID, now)

	approvalID := "apr-" + suffix
	approval := domain.Approval{
		ID:             approvalID,
		ConversationID: conversationID,
		CustomerID:     "customer-" + suffix,
		AgentRunID:     runID,
		Draft: domain.Draft{
			Title:       "无法导出账单",
			Description: "客户反馈导出按钮点击后没有反应。",
			Priority:    domain.PriorityNormal,
		},
		Status:         domain.ApprovalStatusPending,
		IdempotencyKey: domain.DeriveIdempotencyKey(runID),
		CreatedAt:      now,
		ExpiresAt:      now.Add(30 * time.Minute),
	}
	if err := ticketpg.NewRepository(pool).CreateApproval(ctx, approval); err != nil {
		t.Fatalf("CreateApproval returned error: %v", err)
	}
	return approval
}

func confirmCommand(approval domain.Approval, suffix string, now time.Time) domain.ConfirmCommand {
	return domain.ConfirmCommand{
		CustomerID:    approval.CustomerID,
		ApprovalID:    approval.ID,
		JobID:         "job-" + suffix,
		TicketID:      "tkt-" + suffix,
		TicketNumber:  "TK-" + strings.ToUpper(suffix),
		EventID:       "evt-confirm-" + suffix,
		TicketEventID: "evt-created-" + suffix,
		OccurredAt:    now,
	}
}

func executeCommand(command domain.ConfirmCommand) domain.ExecuteCreateCommand {
	return domain.ExecuteCreateCommand{
		ApprovalID:   command.ApprovalID,
		TicketID:     command.TicketID,
		TicketNumber: command.TicketNumber,
		EventID:      command.TicketEventID,
		CreatedAt:    command.OccurredAt,
	}
}

func assertJobCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	approvalID string,
	expected int,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM jobs WHERE job_type = $1 AND idempotency_key = $2",
		domain.CreateJobType,
		approvalID,
	).Scan(&count); err != nil {
		t.Fatalf("count ticket jobs: %v", err)
	}
	if count != expected {
		t.Fatalf("expected %d ticket jobs, got %d", expected, count)
	}
}

func assertRunEventTypeCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runID string,
	eventType string,
	expected int,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM run_events WHERE run_id = $1 AND event_type = $2",
		runID,
		eventType,
	).Scan(&count); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	if count != expected {
		t.Fatalf("expected %d %s events, got %d", expected, eventType, count)
	}
}

func loadCreateCommand(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	approvalID string,
) domain.ExecuteCreateCommand {
	t.Helper()
	var payload []byte
	if err := pool.QueryRow(
		ctx,
		"SELECT payload FROM jobs WHERE job_type = $1 AND idempotency_key = $2",
		domain.CreateJobType,
		approvalID,
	).Scan(&payload); err != nil {
		t.Fatalf("load ticket job: %v", err)
	}
	var command domain.ExecuteCreateCommand
	if err := json.Unmarshal(payload, &command); err != nil {
		t.Fatalf("decode ticket job: %v", err)
	}
	return command
}

func newTicket(id string, approval domain.Approval) domain.Ticket {
	return domain.Ticket{
		ID:             id,
		Number:         "TK-" + strings.ToUpper(approval.ID),
		ConversationID: approval.ConversationID,
		CustomerID:     approval.CustomerID,
		ApprovalID:     approval.ID,
		Draft:          approval.Draft,
		CreatedAt:      approval.CreatedAt,
	}
}

func assertTicketCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	approvalID string,
	expected int,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM tickets WHERE approval_id = $1",
		approvalID,
	).Scan(&count); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if count != expected {
		t.Fatalf("expected %d tickets for %s, got %d", expected, approvalID, count)
	}
}

func seedConversationAndRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	conversationID string,
	runID string,
	now time.Time,
) {
	t.Helper()
	customerID := strings.Replace(conversationID, "conv-", "customer-", 1)
	messageID := "msg-" + conversationID

	statements := []struct {
		sql  string
		args []any
	}{
		{
			`INSERT INTO knowledge_bases (id, name, description, status, created_at, updated_at)
			 VALUES ($1, $1, '', 'active', $2, $2) ON CONFLICT DO NOTHING`,
			[]any{"kb-" + conversationID, now},
		},
		{
			`INSERT INTO conversations (id, customer_id, knowledge_base_id, status, created_at, updated_at)
			 VALUES ($1, $2, $3, 'ai_active', $4, $4)`,
			[]any{conversationID, customerID, "kb-" + conversationID, now},
		},
		{
			`INSERT INTO messages (id, conversation_id, client_message_id, role, content, created_at)
			 VALUES ($1, $2, $3, 'customer', '帮我建个工单', $4)`,
			[]any{messageID, conversationID, "client-" + conversationID, now},
		},
		{
			// completed 状态要求 started_at 与 completed_at 同时存在。
			`INSERT INTO agent_runs (
				id, request_id, conversation_id, source_message_id, status,
				created_at, updated_at, started_at, completed_at
			 )
			 VALUES ($1, $2, $3, $4, 'completed', $5, $5, $5, $5)`,
			[]any{runID, "req-" + runID, conversationID, messageID, now},
		},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed %s: %v", conversationID, err)
		}
	}
}

func openTicketTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	schemaName := fmt.Sprintf("ticket_repository_test_%d", time.Now().UnixNano())
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
