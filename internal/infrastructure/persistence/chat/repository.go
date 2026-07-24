package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domain "agent-chat/internal/domain/chat"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ domain.Repository = (*Repository)(nil)

// Repository 使用 PostgreSQL 事务持久化聊天启动链路。
type Repository struct {
	database *pgxpool.Pool
}

// NewRepository 创建聊天 PostgreSQL Repository。
func NewRepository(database *pgxpool.Pool) *Repository {
	return &Repository{database: database}
}

// CreateConversation 创建绑定客户授权标识和知识库的会话。
func (repository *Repository) CreateConversation(
	ctx context.Context,
	conversation domain.Conversation,
) error {
	if err := conversation.Validate(); err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}
	_, err := repository.database.Exec(ctx, `
		INSERT INTO conversations (
			id,
			customer_id,
			knowledge_base_id,
			status,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
	`,
		conversation.ID,
		conversation.CustomerID,
		conversation.KnowledgeBaseID,
		conversation.Status,
		conversation.CreatedAt,
		conversation.UpdatedAt,
	)
	return mapDatabaseError("create conversation", err)
}

// StartRun 在会话行锁保护下原子创建消息、Run、首事件和持久化 Job。
//
// 会话锁让同一会话内并发提交的 client_message_id 串行化，从而保证幂等重放
// 不会创建多个 Run。已存在的 ID 只有内容完全一致时才返回原结果。
func (repository *Repository) StartRun(
	ctx context.Context,
	submission domain.StartRunSubmission,
) (domain.StartRunResult, error) {
	if err := submission.Validate(); err != nil {
		return domain.StartRunResult{}, fmt.Errorf("start agent run: %w", err)
	}
	eventPayload, err := json.Marshal(submission.Event.Payload)
	if err != nil {
		return domain.StartRunResult{}, errors.New("start agent run: encode event payload")
	}
	jobPayload, err := json.Marshal(map[string]string{"run_id": submission.Run.ID})
	if err != nil {
		return domain.StartRunResult{}, errors.New("start agent run: encode job payload")
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.StartRunResult{}, mapDatabaseError("start agent run", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	var conversationStatus domain.ConversationStatus
	err = transaction.QueryRow(ctx, `
		SELECT status
		FROM conversations
		WHERE id = $1
		  AND customer_id = $2
		FOR UPDATE
	`, submission.Message.ConversationID, submission.CustomerID).Scan(&conversationStatus)
	if err != nil {
		return domain.StartRunResult{}, mapDatabaseError("load conversation for message", err)
	}

	existing, found, err := loadExistingRun(
		ctx,
		transaction,
		submission.Message.ConversationID,
		submission.Message.ClientMessageID,
	)
	if err != nil {
		return domain.StartRunResult{}, err
	}
	if found {
		if existing.Message.Content != submission.Message.Content {
			return domain.StartRunResult{}, fmt.Errorf(
				"start agent run: %w",
				domain.ErrIdempotencyConflict,
			)
		}
		existing.Duplicate = true
		return existing, nil
	}
	if conversationStatus != domain.ConversationStatusAIActive {
		return domain.StartRunResult{}, fmt.Errorf(
			"start agent run: %w",
			domain.ErrInvalidState,
		)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO messages (
			id,
			conversation_id,
			client_message_id,
			role,
			content,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
	`,
		submission.Message.ID,
		submission.Message.ConversationID,
		submission.Message.ClientMessageID,
		submission.Message.Role,
		submission.Message.Content,
		submission.Message.CreatedAt,
	)
	if err != nil {
		return domain.StartRunResult{}, mapDatabaseError("create customer message", err)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO agent_runs (
			id,
			request_id,
			conversation_id,
			source_message_id,
			status,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		submission.Run.ID,
		submission.Run.RequestID,
		submission.Run.ConversationID,
		submission.Run.SourceMessageID,
		submission.Run.Status,
		submission.Run.CreatedAt,
		submission.Run.UpdatedAt,
	)
	if err != nil {
		return domain.StartRunResult{}, mapDatabaseError("create agent run", err)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO run_events (
			id,
			run_id,
			sequence,
			event_type,
			payload,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
	`,
		submission.Event.ID,
		submission.Event.RunID,
		submission.Event.Sequence,
		submission.Event.Type,
		eventPayload,
		submission.Event.CreatedAt,
	)
	if err != nil {
		return domain.StartRunResult{}, mapDatabaseError("create initial run event", err)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO jobs (
			id,
			job_type,
			idempotency_key,
			payload,
			status
		)
		VALUES ($1, $2, $3, $4, 'pending')
	`,
		submission.JobID,
		domain.AgentRunJobType,
		submission.Run.ID,
		jobPayload,
	)
	if err != nil {
		return domain.StartRunResult{}, mapDatabaseError("create agent run job", err)
	}

	commandTag, err := transaction.Exec(ctx, `
		UPDATE conversations
		SET last_message_at = GREATEST(
		        COALESCE(last_message_at, $2),
		        $2
		    ),
		    updated_at = GREATEST(updated_at, $2)
		WHERE id = $1
	`, submission.Message.ConversationID, submission.Message.CreatedAt)
	if err != nil {
		return domain.StartRunResult{}, mapDatabaseError("update conversation activity", err)
	}
	if commandTag.RowsAffected() != 1 {
		return domain.StartRunResult{}, fmt.Errorf(
			"update conversation activity: %w",
			domain.ErrNotFound,
		)
	}

	if err := transaction.Commit(ctx); err != nil {
		return domain.StartRunResult{}, mapDatabaseError("start agent run", err)
	}
	return domain.StartRunResult{
		Message: submission.Message,
		Run:     submission.Run,
	}, nil
}

func loadExistingRun(
	ctx context.Context,
	transaction pgx.Tx,
	conversationID string,
	clientMessageID string,
) (domain.StartRunResult, bool, error) {
	var result domain.StartRunResult
	var role string
	err := transaction.QueryRow(ctx, `
		SELECT
			message.id,
			message.conversation_id,
			message.client_message_id,
			message.role,
			message.content,
			message.created_at,
			run.id,
			run.request_id,
			run.conversation_id,
			run.source_message_id,
			run.status,
			run.created_at,
			run.updated_at
		FROM messages AS message
		JOIN agent_runs AS run
		  ON run.source_message_id = message.id
		WHERE message.conversation_id = $1
		  AND message.client_message_id = $2
	`, conversationID, clientMessageID).Scan(
		&result.Message.ID,
		&result.Message.ConversationID,
		&result.Message.ClientMessageID,
		&role,
		&result.Message.Content,
		&result.Message.CreatedAt,
		&result.Run.ID,
		&result.Run.RequestID,
		&result.Run.ConversationID,
		&result.Run.SourceMessageID,
		&result.Run.Status,
		&result.Run.CreatedAt,
		&result.Run.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StartRunResult{}, false, nil
	}
	if err != nil {
		return domain.StartRunResult{}, false, mapDatabaseError("load existing agent run", err)
	}
	result.Message.Role = domain.MessageRole(role)
	if err := result.Message.Validate(); err != nil {
		return domain.StartRunResult{}, false, errors.New(
			"load existing agent run: invalid persisted message",
		)
	}
	if err := result.Run.ValidateSnapshot(); err != nil {
		return domain.StartRunResult{}, false, errors.New(
			"load existing agent run: invalid persisted run",
		)
	}
	return result, true, nil
}

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
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
		case "23503":
			return fmt.Errorf("%s: %w", operation, domain.ErrNotFound)
		case "23514":
			return fmt.Errorf("%s: %w", operation, domain.ErrInvalidState)
		}
	}
	// PostgreSQL 原始错误可能包含 SQL、Schema 或消息内容，不跨越 Infrastructure 边界。
	return fmt.Errorf("%s: database operation failed", strings.TrimSpace(operation))
}
