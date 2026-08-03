package ticketpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	chatdomain "agent-chat/internal/domain/chat"
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

// ConfirmAndEnqueue 原子确认审批、创建 ticket.create Job 并追加 Run 事件。
//
// 过期判定使用 clock_timestamp() 与调用方时刻中的较晚者。数据库时钟防止 API
// 节点时间落后而放行过期授权，调用方时刻则让测试和显式向前校时保持保守。
func (repository *Repository) ConfirmAndEnqueue(
	ctx context.Context,
	command domain.ConfirmCommand,
) (domain.ApproveResult, error) {
	if err := command.Validate(); err != nil {
		return domain.ApproveResult{}, fmt.Errorf(
			"confirm ticket approval: %w: %w",
			domain.ErrInvalidCommand,
			err,
		)
	}
	command.CustomerID = strings.TrimSpace(command.CustomerID)
	command.ApprovalID = strings.TrimSpace(command.ApprovalID)

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.ApproveResult{}, mapDatabaseError("confirm ticket approval", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	current, err := loadForUpdate(ctx, transaction, command.CustomerID, command.ApprovalID)
	if err != nil {
		return domain.ApproveResult{}, err
	}
	effectiveNow, err := effectiveDatabaseTime(ctx, transaction, command.OccurredAt)
	if err != nil {
		return domain.ApproveResult{}, err
	}

	switch current.Status {
	case domain.ApprovalStatusApproved:
		result := domain.ApproveResult{Approval: current, AlreadyApproved: true}
		existing, loadErr := scanTicket(transaction.QueryRow(ctx, ticketByApprovalSQL, current.ID))
		if loadErr == nil {
			result.Ticket = &existing
		} else if !errors.Is(loadErr, domain.ErrNotFound) {
			return domain.ApproveResult{}, loadErr
		}
		return result, nil
	case domain.ApprovalStatusCancelled:
		return domain.ApproveResult{}, fmt.Errorf(
			"approve ticket: %w: approval was cancelled",
			domain.ErrInvalidState,
		)
	case domain.ApprovalStatusExpired:
		return domain.ApproveResult{}, fmt.Errorf("approve ticket: %w", domain.ErrExpired)
	}

	if current.ExpiredAt(effectiveNow) {
		// 越过窗口的 pending 记录在此落为终态，避免它长期停留在可确认的外观。
		if err := markExpired(ctx, transaction, command.ApprovalID, effectiveNow); err != nil {
			return domain.ApproveResult{}, err
		}
		if err := appendApprovalEvent(
			ctx,
			transaction,
			current.AgentRunID,
			command.EventID,
			chatdomain.EventTypeApprovalExpired,
			map[string]any{"approvalId": current.ID},
			effectiveNow,
		); err != nil {
			return domain.ApproveResult{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return domain.ApproveResult{}, mapDatabaseError("confirm ticket approval", err)
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
	`, command.ApprovalID, effectiveNow)
	if err != nil {
		return domain.ApproveResult{}, mapDatabaseError("confirm ticket approval", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ApproveResult{}, fmt.Errorf(
			"approve ticket: %w: approval is no longer pending",
			domain.ErrInvalidState,
		)
	}
	jobPayload, err := json.Marshal(domain.ExecuteCreateCommand{
		ApprovalID:   current.ID,
		TicketID:     command.TicketID,
		TicketNumber: command.TicketNumber,
		EventID:      command.TicketEventID,
		CreatedAt:    effectiveNow,
	})
	if err != nil {
		return domain.ApproveResult{}, errors.New("confirm ticket approval: encode job payload")
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO jobs (
			id,
			job_type,
			idempotency_key,
			payload,
			status,
			available_at,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, 'pending', $5, $5, $5)
	`,
		command.JobID,
		domain.CreateJobType,
		current.ID,
		jobPayload,
		effectiveNow,
	)
	if err != nil {
		return domain.ApproveResult{}, mapDatabaseError("create ticket job", err)
	}
	if err := appendApprovalEvent(
		ctx,
		transaction,
		current.AgentRunID,
		command.EventID,
		chatdomain.EventTypeApprovalConfirmed,
		map[string]any{
			"approvalId": current.ID,
			"jobId":      command.JobID,
		},
		effectiveNow,
	); err != nil {
		return domain.ApproveResult{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.ApproveResult{}, mapDatabaseError("confirm ticket approval", err)
	}

	current.Status = domain.ApprovalStatusApproved
	decidedAt := effectiveNow
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
	eventID string,
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
	effectiveNow, err := effectiveDatabaseTime(ctx, transaction, now)
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
	case domain.ApprovalStatusExpired:
		return domain.Approval{}, fmt.Errorf("cancel ticket: %w", domain.ErrExpired)
	}
	if current.ExpiredAt(effectiveNow) {
		if err := markExpired(ctx, transaction, approvalID, effectiveNow); err != nil {
			return domain.Approval{}, err
		}
		if err := appendApprovalEvent(
			ctx,
			transaction,
			current.AgentRunID,
			eventID,
			chatdomain.EventTypeApprovalExpired,
			map[string]any{"approvalId": current.ID},
			effectiveNow,
		); err != nil {
			return domain.Approval{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return domain.Approval{}, mapDatabaseError("cancel ticket", err)
		}
		return domain.Approval{}, fmt.Errorf("cancel ticket: %w", domain.ErrExpired)
	}

	tag, err := transaction.Exec(ctx, `
		UPDATE ticket_approvals
		SET status = 'cancelled',
		    decided_at = $2
		WHERE id = $1
		  AND status = 'pending'
	`, approvalID, effectiveNow)
	if err != nil {
		return domain.Approval{}, mapDatabaseError("cancel ticket", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.Approval{}, fmt.Errorf(
			"cancel ticket: %w: approval is no longer cancellable",
			domain.ErrInvalidState,
		)
	}
	if err := appendApprovalEvent(
		ctx,
		transaction,
		current.AgentRunID,
		eventID,
		chatdomain.EventTypeApprovalCancelled,
		map[string]any{"approvalId": current.ID},
		effectiveNow,
	); err != nil {
		return domain.Approval{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.Approval{}, mapDatabaseError("cancel ticket", err)
	}

	current.Status = domain.ApprovalStatusCancelled
	decidedAt := effectiveNow
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

// ExecuteCreateTicket 幂等执行 ticket.create Job，并原子追加 ticket.created 事件。
func (repository *Repository) ExecuteCreateTicket(
	ctx context.Context,
	command domain.ExecuteCreateCommand,
) (domain.Ticket, error) {
	if err := command.Validate(); err != nil {
		return domain.Ticket{}, fmt.Errorf("execute ticket creation: %w: %w", domain.ErrInvalidCommand, err)
	}
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Ticket{}, mapDatabaseError("execute ticket creation", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	approval, err := loadForUpdateByID(ctx, transaction, command.ApprovalID)
	if err != nil {
		return domain.Ticket{}, err
	}
	if approval.Status != domain.ApprovalStatusApproved {
		return domain.Ticket{}, fmt.Errorf(
			"execute ticket creation: %w: approval is not approved",
			domain.ErrInvalidState,
		)
	}
	existing, err := scanTicket(transaction.QueryRow(ctx, ticketByApprovalSQL, approval.ID))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Ticket{}, err
	}

	item := domain.Ticket{
		ID:             command.TicketID,
		Number:         command.TicketNumber,
		ConversationID: approval.ConversationID,
		CustomerID:     approval.CustomerID,
		ApprovalID:     approval.ID,
		Draft:          approval.Draft,
		CreatedAt:      command.CreatedAt,
	}
	if err := item.Validate(); err != nil {
		return domain.Ticket{}, fmt.Errorf("execute ticket creation: %w: %w", domain.ErrInvalidCommand, err)
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
		item.Draft.Priority,
		item.CreatedAt,
	)
	if err != nil {
		return domain.Ticket{}, mapDatabaseError("execute ticket creation", err)
	}
	if err := appendApprovalEvent(
		ctx,
		transaction,
		approval.AgentRunID,
		command.EventID,
		chatdomain.EventTypeTicketCreated,
		map[string]any{
			"approvalId":   approval.ID,
			"ticketId":     item.ID,
			"ticketNumber": item.Number,
		},
		command.CreatedAt,
	); err != nil {
		return domain.Ticket{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.Ticket{}, mapDatabaseError("execute ticket creation", err)
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

func loadForUpdateByID(
	ctx context.Context,
	transaction pgx.Tx,
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
		FOR UPDATE
	`, strings.TrimSpace(approvalID)))
}

func effectiveDatabaseTime(
	ctx context.Context,
	transaction pgx.Tx,
	callerTime time.Time,
) (time.Time, error) {
	var effective time.Time
	if err := transaction.QueryRow(ctx, `
		SELECT GREATEST(clock_timestamp(), $1::timestamptz)
	`, callerTime).Scan(&effective); err != nil {
		return time.Time{}, mapDatabaseError("read database time", err)
	}
	return effective.UTC(), nil
}

func appendApprovalEvent(
	ctx context.Context,
	transaction pgx.Tx,
	runID string,
	eventID string,
	eventType chatdomain.EventType,
	payload map[string]any,
	createdAt time.Time,
) error {
	if strings.TrimSpace(eventID) == "" || len(strings.TrimSpace(eventID)) > 64 {
		return fmt.Errorf("append approval event: %w: invalid event ID", domain.ErrInvalidCommand)
	}
	var lockedRunID string
	if err := transaction.QueryRow(ctx, `
		SELECT id FROM agent_runs WHERE id = $1 FOR UPDATE
	`, runID).Scan(&lockedRunID); err != nil {
		return mapDatabaseError("lock run for approval event", err)
	}
	var sequence int
	if err := transaction.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = $1
	`, runID).Scan(&sequence); err != nil {
		return mapDatabaseError("allocate approval event sequence", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return errors.New("append approval event: encode payload")
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO run_events (id, run_id, sequence, event_type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, eventID, runID, sequence, eventType, encoded, createdAt)
	if err != nil {
		return mapDatabaseError("append approval event", err)
	}
	return nil
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
