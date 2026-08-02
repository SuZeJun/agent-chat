package ticketpg_test

import (
	"context"
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

		if _, err := repository.Cancel(ctx, approval.CustomerID, approval.ID, now); err != nil {
			t.Fatalf("Cancel returned error: %v", err)
		}
		// 取消后确认必须失败。
		if _, err := repository.Approve(ctx, approval.CustomerID, approval.ID, now); !errors.Is(
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
	})

	t.Run("过期确认不执行写操作", func(t *testing.T) {
		approval := seedApproval(t, ctx, pool, "expire-1", now)

		afterExpiry := approval.ExpiresAt.Add(time.Second)
		if _, err := repository.Approve(
			ctx,
			approval.CustomerID,
			approval.ID,
			afterExpiry,
		); !errors.Is(err, domain.ErrExpired) {
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
	})

	t.Run("重复确认不产生重复副作用", func(t *testing.T) {
		approval := seedApproval(t, ctx, pool, "approve-1", now)

		first, err := repository.Approve(ctx, approval.CustomerID, approval.ID, now)
		if err != nil {
			t.Fatalf("first Approve returned error: %v", err)
		}
		if first.AlreadyApproved {
			t.Fatal("first approval must not report AlreadyApproved")
		}
		created, err := repository.CreateTicket(ctx, newTicket("tkt-approve", approval))
		if err != nil {
			t.Fatalf("CreateTicket returned error: %v", err)
		}

		// 第二次确认必须报告已确认，且不产生第二张工单。
		second, err := repository.Approve(ctx, approval.CustomerID, approval.ID, now)
		if err != nil {
			t.Fatalf("second Approve returned error: %v", err)
		}
		if !second.AlreadyApproved {
			t.Fatal("repeated approval must report AlreadyApproved")
		}
		again, err := repository.CreateTicket(ctx, newTicket("tkt-approve-2", approval))
		if err != nil {
			t.Fatalf("repeated CreateTicket returned error: %v", err)
		}
		if again.ID != created.ID || again.Number != created.Number {
			t.Fatalf("repeated creation produced a different ticket: %#v vs %#v", again, created)
		}
		assertTicketCount(t, ctx, pool, approval.ID, 1)
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
			if _, err := repository.Approve(ctx, approval.CustomerID, approval.ID, now); err != nil {
				results <- err
				return
			}
			_, err := repository.CreateTicket(
				ctx,
				newTicket(fmt.Sprintf("tkt-concurrent-%d", index), approval),
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
	assertTicketCount(t, ctx, pool, approval.ID, 1)
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
		IdempotencyKey: domain.DeriveIdempotencyKey(approvalID),
		CreatedAt:      now,
		ExpiresAt:      now.Add(30 * time.Minute),
	}
	if err := ticketpg.NewRepository(pool).CreateApproval(ctx, approval); err != nil {
		t.Fatalf("CreateApproval returned error: %v", err)
	}
	return approval
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
