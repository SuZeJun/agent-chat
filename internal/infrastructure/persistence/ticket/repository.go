package ticketpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "agent-chat/internal/domain/ticket"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ domain.Repository = (*Repository)(nil)

// Repository 使用 PostgreSQL 持久化工单审批与工单记录。
type Repository struct {
	database *pgxpool.Pool
}

// NewRepository 创建工单 PostgreSQL Repository。
func NewRepository(database *pgxpool.Pool) *Repository {
	return &Repository{database: database}
}

// CreateApproval 保存待确认的工单草稿。
//
// 只写审批表：草稿阶段不得产生任何工单副作用，这是「未确认不执行写操作」
// 在持久化层的体现。
func (repository *Repository) CreateApproval(
	ctx context.Context,
	approval domain.Approval,
) error {
	if err := approval.Validate(); err != nil {
		return fmt.Errorf("create ticket approval: %w: %w", domain.ErrInvalidCommand, err)
	}
	if approval.Status != domain.ApprovalStatusPending {
		return fmt.Errorf(
			"create ticket approval: %w: new approval must be pending",
			domain.ErrInvalidCommand,
		)
	}

	_, err := repository.database.Exec(ctx, `
		INSERT INTO ticket_approvals (
			id,
			conversation_id,
			customer_id,
			agent_run_id,
			title,
			description,
			priority,
			status,
			idempotency_key,
			created_at,
			expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9, $10)
	`,
		approval.ID,
		approval.ConversationID,
		approval.CustomerID,
		approval.AgentRunID,
		approval.Draft.Title,
		approval.Draft.Description,
		string(approval.Draft.Priority),
		approval.IdempotencyKey,
		approval.CreatedAt,
		approval.ExpiresAt,
	)
	if err != nil {
		return mapDatabaseError("create ticket approval", err)
	}
	return nil
}

// LoadApproval 按客户授权范围读取审批。
func (repository *Repository) LoadApproval(
	ctx context.Context,
	customerID string,
	approvalID string,
) (domain.Approval, error) {
	return scanApproval(repository.database.QueryRow(ctx, `
		SELECT
			id,
			conversation_id,
			customer_id,
			agent_run_id,
			title,
			description,
			priority,
			status,
			idempotency_key,
			COALESCE(
				(SELECT ticket.id FROM tickets AS ticket WHERE ticket.approval_id = approval.id),
				''
			),
			created_at,
			expires_at,
			decided_at
		FROM ticket_approvals AS approval
		WHERE id = $1
		  AND customer_id = $2
	`, strings.TrimSpace(approvalID), strings.TrimSpace(customerID)))
}

// Approve 原子地把 pending 转为 approved。
//
// 过期判定用数据库时间与传入时刻中的较严者：调用方时钟不可信，而窗口边界直接
// 决定「过期确认不执行写操作」是否成立。
func (repository *Repository) Approve(
	ctx context.Context,
	customerID string,
	approvalID string,
	now time.Time,
) (domain.ApproveResult, error) {
	customerID = strings.TrimSpace(customerID)
	approvalID = strings.TrimSpace(approvalID)

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.ApproveResult{}, mapDatabaseError("approve ticket", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	current, err := loadForUpdate(ctx, transaction, customerID, approvalID)
	if err != nil {
		return domain.ApproveResult{}, err
	}

	switch current.Status {
	case domain.ApprovalStatusApproved:
		// 重复确认不是错误：调用方据此返回首次结果而不重复创建工单。
		return domain.ApproveResult{Approval: current, AlreadyApproved: true}, nil
	case domain.ApprovalStatusCancelled:
		return domain.ApproveResult{}, fmt.Errorf(
			"approve ticket: %w: approval was cancelled",
			domain.ErrInvalidState,
		)
	case domain.ApprovalStatusExpired:
		return domain.ApproveResult{}, fmt.Errorf("approve ticket: %w", domain.ErrExpired)
	}

	if current.ExpiredAt(now) {
		// 越过窗口的 pending 记录在此落为终态，避免它长期停留在可确认的外观。
		if err := markExpired(ctx, transaction, approvalID, now); err != nil {
			return domain.ApproveResult{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return domain.ApproveResult{}, mapDatabaseError("approve ticket", err)
		}
		return domain.ApproveResult{}, fmt.Errorf("approve ticket: %w", domain.ErrExpired)
	}

	// 条件更新是并发下的唯一保证：两个请求同时到达时只有一个能影响到行。
	tag, err := transaction.Exec(ctx, `
		UPDATE ticket_approvals
		SET status = 'approved',
		    decided_at = $2
		WHERE id = $1
		  AND status = 'pending'
		  AND expires_at > $2
	`, approvalID, now)
	if err != nil {
		return domain.ApproveResult{}, mapDatabaseError("approve ticket", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ApproveResult{}, fmt.Errorf(
			"approve ticket: %w: approval is no longer pending",
			domain.ErrInvalidState,
		)
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.ApproveResult{}, mapDatabaseError("approve ticket", err)
	}

	current.Status = domain.ApprovalStatusApproved
	decidedAt := now
	current.DecidedAt = &decidedAt
	return domain.ApproveResult{Approval: current}, nil
}

// Cancel 原子地把 pending 转为 cancelled。
//
// 已取消视为成功：取消是幂等的用户意图，重复取消不应报错。
func (repository *Repository) Cancel(
	ctx context.Context,
	customerID string,
	approvalID string,
	now time.Time,
) (domain.Approval, error) {
	customerID = strings.TrimSpace(customerID)
	approvalID = strings.TrimSpace(approvalID)

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Approval{}, mapDatabaseError("cancel ticket", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	current, err := loadForUpdate(ctx, transaction, customerID, approvalID)
	if err != nil {
		return domain.Approval{}, err
	}
	switch current.Status {
	case domain.ApprovalStatusCancelled:
		return current, nil
	case domain.ApprovalStatusApproved:
		// 已确认的写操作可能已经执行，取消无法回滚副作用，必须明确拒绝。
		return domain.Approval{}, fmt.Errorf(
			"cancel ticket: %w: approval was already approved",
			domain.ErrInvalidState,
		)
	}

	tag, err := transaction.Exec(ctx, `
		UPDATE ticket_approvals
		SET status = 'cancelled',
		    decided_at = $2
		WHERE id = $1
		  AND status IN ('pending', 'expired')
	`, approvalID, now)
	if err != nil {
		return domain.Approval{}, mapDatabaseError("cancel ticket", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.Approval{}, fmt.Errorf(
			"cancel ticket: %w: approval is no longer cancellable",
			domain.ErrInvalidState,
		)
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.Approval{}, mapDatabaseError("cancel ticket", err)
	}

	current.Status = domain.ApprovalStatusCancelled
	decidedAt := now
	current.DecidedAt = &decidedAt
	return current, nil
}

// CreateTicket 创建工单，同一审批重复调用返回既有工单。
//
// 幂等由 tickets.approval_id 的唯一约束保证。即便应用层的状态守卫被绕过，
// 数据库仍会拒绝第二次写入——这是「重复确认不产生重复副作用」的最后防线。
func (repository *Repository) CreateTicket(
	ctx context.Context,
	item domain.Ticket,
) (domain.Ticket, error) {
	if err := item.Validate(); err != nil {
		return domain.Ticket{}, fmt.Errorf("create ticket: %w: %w", domain.ErrInvalidCommand, err)
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Ticket{}, mapDatabaseError("create ticket", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	var status string
	err = transaction.QueryRow(ctx, `
		SELECT status
		FROM ticket_approvals
		WHERE id = $1
		  AND customer_id = $2
		FOR UPDATE
	`, item.ApprovalID, item.CustomerID).Scan(&status)
	if err != nil {
		return domain.Ticket{}, mapDatabaseError("load approval for ticket", err)
	}
	// 只有已确认的审批能产生工单。这条检查与状态转换处的守卫互相独立：
	// 任何绕过转换直接调用创建的路径都会在此被拦下。
	if domain.ApprovalStatus(status) != domain.ApprovalStatusApproved {
		return domain.Ticket{}, fmt.Errorf(
			"create ticket: %w: approval is not approved",
			domain.ErrInvalidState,
		)
	}

	existing, err := scanTicket(transaction.QueryRow(ctx, ticketByApprovalSQL, item.ApprovalID))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Ticket{}, err
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO tickets (
			id,
			number,
			conversation_id,
			customer_id,
			approval_id,
			title,
			description,
			priority,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		item.ID,
		item.Number,
		item.ConversationID,
		item.CustomerID,
		item.ApprovalID,
		item.Draft.Title,
		item.Draft.Description,
		string(item.Draft.Priority),
		item.CreatedAt,
	)
	if err != nil {
		return domain.Ticket{}, mapDatabaseError("create ticket", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.Ticket{}, mapDatabaseError("create ticket", err)
	}
	return item, nil
}

// LoadTicketByApproval 读取审批已产生的工单。
func (repository *Repository) LoadTicketByApproval(
	ctx context.Context,
	approvalID string,
) (domain.Ticket, error) {
	return scanTicket(repository.database.QueryRow(
		ctx,
		ticketByApprovalSQL,
		strings.TrimSpace(approvalID),
	))
}

const ticketByApprovalSQL = `
	SELECT
		id,
		number,
		conversation_id,
		customer_id,
		approval_id,
		title,
		description,
		priority,
		created_at
	FROM tickets
	WHERE approval_id = $1
`

func loadForUpdate(
	ctx context.Context,
	transaction pgx.Tx,
	customerID string,
	approvalID string,
) (domain.Approval, error) {
	return scanApproval(transaction.QueryRow(ctx, `
		SELECT
			id,
			conversation_id,
			customer_id,
			agent_run_id,
			title,
			description,
			priority,
			status,
			idempotency_key,
			COALESCE(
				(SELECT ticket.id FROM tickets AS ticket WHERE ticket.approval_id = approval.id),
				''
			),
			created_at,
			expires_at,
			decided_at
		FROM ticket_approvals AS approval
		WHERE id = $1
		  AND customer_id = $2
		FOR UPDATE
	`, approvalID, customerID))
}

func markExpired(
	ctx context.Context,
	transaction pgx.Tx,
	approvalID string,
	now time.Time,
) error {
	_, err := transaction.Exec(ctx, `
		UPDATE ticket_approvals
		SET status = 'expired',
		    decided_at = $2
		WHERE id = $1
		  AND status = 'pending'
	`, approvalID, now)
	if err != nil {
		return mapDatabaseError("expire ticket approval", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanApproval(row rowScanner) (domain.Approval, error) {
	var approval domain.Approval
	var priority string
	var status string
	err := row.Scan(
		&approval.ID,
		&approval.ConversationID,
		&approval.CustomerID,
		&approval.AgentRunID,
		&approval.Draft.Title,
		&approval.Draft.Description,
		&priority,
		&status,
		&approval.IdempotencyKey,
		&approval.TicketID,
		&approval.CreatedAt,
		&approval.ExpiresAt,
		&approval.DecidedAt,
	)
	if err != nil {
		return domain.Approval{}, mapDatabaseError("load ticket approval", err)
	}
	approval.Draft.Priority = domain.Priority(priority)
	approval.Status = domain.ApprovalStatus(status)
	return approval, nil
}

func scanTicket(row rowScanner) (domain.Ticket, error) {
	var item domain.Ticket
	var priority string
	err := row.Scan(
		&item.ID,
		&item.Number,
		&item.ConversationID,
		&item.CustomerID,
		&item.ApprovalID,
		&item.Draft.Title,
		&item.Draft.Description,
		&priority,
		&item.CreatedAt,
	)
	if err != nil {
		return domain.Ticket{}, mapDatabaseError("load ticket", err)
	}
	item.Draft.Priority = domain.Priority(priority)
	return item, nil
}

// mapDatabaseError 把驱动错误转成领域错误，不泄露 SQL 或数据片段。
func mapDatabaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, domain.ErrNotFound)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		// 唯一约束冲突意味着并发路径已经写入，调用方应重新读取既有记录。
		return fmt.Errorf("%s: %w", operation, domain.ErrInvalidState)
	}
	return fmt.Errorf("%s: database operation failed", operation)
}
