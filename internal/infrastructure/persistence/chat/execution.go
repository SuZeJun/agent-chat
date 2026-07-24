package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domain "agent-chat/internal/domain/chat"

	"github.com/jackc/pgx/v5"
)

// BeginRunAttempt 将 pending/running Run 原子切换为 running 并追加 run.started。
func (repository *Repository) BeginRunAttempt(
	ctx context.Context,
	command domain.BeginRunAttempt,
) (domain.RunSource, error) {
	if err := command.Validate(); err != nil {
		return domain.RunSource{}, fmt.Errorf("begin agent run attempt: %w", err)
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.RunSource{}, mapDatabaseError("begin agent run attempt", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	source, err := loadRunSourceForUpdate(ctx, transaction, command.RunID)
	if err != nil {
		return domain.RunSource{}, err
	}
	if source.Terminal() {
		return source, nil
	}
	if source.Conversation != domain.ConversationStatusAIActive {
		return domain.RunSource{}, fmt.Errorf(
			"begin agent run attempt: %w",
			domain.ErrInvalidState,
		)
	}
	if source.Run.Status != domain.RunStatusPending &&
		source.Run.Status != domain.RunStatusRunning {
		return domain.RunSource{}, fmt.Errorf(
			"begin agent run attempt: %w",
			domain.ErrInvalidState,
		)
	}

	commandTag, err := transaction.Exec(ctx, `
		UPDATE agent_runs
		SET status = 'running',
		    started_at = COALESCE(started_at, $2),
		    updated_at = GREATEST(updated_at, $2)
		WHERE id = $1
		  AND status IN ('pending', 'running')
	`, command.RunID, command.Event.CreatedAt)
	if err != nil {
		return domain.RunSource{}, mapDatabaseError("mark agent run running", err)
	}
	if commandTag.RowsAffected() != 1 {
		return domain.RunSource{}, fmt.Errorf(
			"mark agent run running: %w",
			domain.ErrInvalidState,
		)
	}
	if err := appendRunEvents(
		ctx,
		transaction,
		command.RunID,
		[]domain.EventDraft{command.Event},
	); err != nil {
		return domain.RunSource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.RunSource{}, mapDatabaseError("begin agent run attempt", err)
	}
	source.Run.Status = domain.RunStatusRunning
	source.Run.UpdatedAt = command.Event.CreatedAt
	return source, nil
}

// CompleteRun 原子保存 Assistant Message、Graph Result、事件并结束 Run。
func (repository *Repository) CompleteRun(
	ctx context.Context,
	command domain.CompleteRunCommand,
) error {
	if err := command.Validate(); err != nil {
		return fmt.Errorf("complete agent run: %w", err)
	}
	result, err := json.Marshal(command.Result)
	if err != nil {
		return errors.New("complete agent run: encode result")
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("complete agent run", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	source, err := loadRunSourceForUpdate(ctx, transaction, command.RunID)
	if err != nil {
		return err
	}
	if source.Run.Status == domain.RunStatusCompleted {
		return nil
	}
	if source.Run.Status != domain.RunStatusRunning ||
		source.Conversation != domain.ConversationStatusAIActive ||
		command.Message.ConversationID != source.Run.ConversationID {
		return fmt.Errorf("complete agent run: %w", domain.ErrInvalidState)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO messages (
			id,
			conversation_id,
			client_message_id,
			agent_run_id,
			role,
			content,
			created_at
		)
		VALUES ($1, $2, '', $3, $4, $5, $6)
	`,
		command.Message.ID,
		command.Message.ConversationID,
		command.Message.AgentRunID,
		command.Message.Role,
		command.Message.Content,
		command.Message.CreatedAt,
	)
	if err != nil {
		return mapDatabaseError("create assistant message", err)
	}
	if err := appendRunEvents(
		ctx,
		transaction,
		command.RunID,
		command.Events,
	); err != nil {
		return err
	}

	commandTag, err := transaction.Exec(ctx, `
		UPDATE agent_runs
		SET status = 'completed',
		    result = $2,
		    error_code = '',
		    completed_at = $3,
		    updated_at = GREATEST(updated_at, $3)
		WHERE id = $1
		  AND status = 'running'
	`, command.RunID, result, command.CompletedAt)
	if err != nil {
		return mapDatabaseError("mark agent run completed", err)
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("mark agent run completed: %w", domain.ErrInvalidState)
	}

	_, err = transaction.Exec(ctx, `
		UPDATE conversations
		SET last_message_at = GREATEST(
		        COALESCE(last_message_at, $2),
		        $2
		    ),
		    updated_at = GREATEST(updated_at, $2)
		WHERE id = $1
	`, command.Message.ConversationID, command.Message.CreatedAt)
	if err != nil {
		return mapDatabaseError("update conversation with assistant message", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return mapDatabaseError("complete agent run", err)
	}
	return nil
}

// RecordRunFailure 追加尝试失败事件，并仅在 Terminal 时结束 Run。
func (repository *Repository) RecordRunFailure(
	ctx context.Context,
	command domain.RecordRunFailureCommand,
) error {
	if err := command.Validate(); err != nil {
		return fmt.Errorf("record agent run failure: %w", err)
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("record agent run failure", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	var status domain.RunStatus
	err = transaction.QueryRow(ctx, `
		SELECT status
		FROM agent_runs
		WHERE id = $1
		FOR UPDATE
	`, command.RunID).Scan(&status)
	if err != nil {
		return mapDatabaseError("load agent run for failure", err)
	}
	if status == domain.RunStatusCompleted || status == domain.RunStatusFailed {
		return nil
	}
	if status != domain.RunStatusPending && status != domain.RunStatusRunning {
		return fmt.Errorf("record agent run failure: %w", domain.ErrInvalidState)
	}

	if command.Terminal {
		_, err = transaction.Exec(ctx, `
			UPDATE agent_runs
			SET status = 'failed',
			    error_code = $2,
			    started_at = COALESCE(started_at, $3),
			    completed_at = GREATEST(COALESCE(started_at, $3), $3),
			    updated_at = GREATEST(updated_at, $3)
			WHERE id = $1
			  AND status IN ('pending', 'running')
		`, command.RunID, command.ErrorCode, command.OccurredAt)
	} else {
		_, err = transaction.Exec(ctx, `
			UPDATE agent_runs
			SET status = 'running',
			    started_at = COALESCE(started_at, $2),
			    updated_at = GREATEST(updated_at, $2)
			WHERE id = $1
			  AND status IN ('pending', 'running')
		`, command.RunID, command.OccurredAt)
	}
	if err != nil {
		return mapDatabaseError("update agent run failure", err)
	}
	if err := appendRunEvents(
		ctx,
		transaction,
		command.RunID,
		[]domain.EventDraft{command.Event},
	); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return mapDatabaseError("record agent run failure", err)
	}
	return nil
}

func loadRunSourceForUpdate(
	ctx context.Context,
	transaction pgx.Tx,
	runID string,
) (domain.RunSource, error) {
	var source domain.RunSource
	var role string
	err := transaction.QueryRow(ctx, `
		SELECT
			run.id,
			run.conversation_id,
			run.source_message_id,
			run.status,
			run.created_at,
			run.updated_at,
			message.id,
			message.conversation_id,
			message.client_message_id,
			COALESCE(message.agent_run_id, ''),
			message.role,
			message.content,
			message.created_at,
			conversation.knowledge_base_id,
			conversation.status
		FROM agent_runs AS run
		JOIN messages AS message
		  ON message.id = run.source_message_id
		JOIN conversations AS conversation
		  ON conversation.id = run.conversation_id
		WHERE run.id = $1
		FOR UPDATE OF run, conversation
	`, runID).Scan(
		&source.Run.ID,
		&source.Run.ConversationID,
		&source.Run.SourceMessageID,
		&source.Run.Status,
		&source.Run.CreatedAt,
		&source.Run.UpdatedAt,
		&source.Message.ID,
		&source.Message.ConversationID,
		&source.Message.ClientMessageID,
		&source.Message.AgentRunID,
		&role,
		&source.Message.Content,
		&source.Message.CreatedAt,
		&source.KnowledgeBaseID,
		&source.Conversation,
	)
	if err != nil {
		return domain.RunSource{}, mapDatabaseError("load agent run source", err)
	}
	source.Message.Role = domain.MessageRole(role)
	if err := source.Validate(); err != nil {
		return domain.RunSource{}, errors.New(
			"load agent run source: invalid persisted source",
		)
	}
	return source, nil
}

func appendRunEvents(
	ctx context.Context,
	transaction pgx.Tx,
	runID string,
	events []domain.EventDraft,
) error {
	var nextSequence int
	if err := transaction.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM run_events
		WHERE run_id = $1
	`, runID).Scan(&nextSequence); err != nil {
		return mapDatabaseError("allocate run event sequence", err)
	}
	for index, event := range events {
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return errors.New("append run events: encode payload")
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
			event.ID,
			runID,
			nextSequence+index,
			event.Type,
			payload,
			event.CreatedAt,
		)
		if err != nil {
			return mapDatabaseError("append run event", err)
		}
	}
	return nil
}
