package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	domain "agent-chat/internal/domain/chat"

	"github.com/jackc/pgx/v5"
)

// LoadMessageHistory 在客户作用域内读取最近一页消息及关联 Run 快照。
func (repository *Repository) LoadMessageHistory(
	ctx context.Context,
	query domain.MessageHistoryQuery,
) (domain.MessageHistoryPage, error) {
	query.CustomerID = strings.TrimSpace(query.CustomerID)
	query.ConversationID = strings.TrimSpace(query.ConversationID)
	query.BeforeMessageID = strings.TrimSpace(query.BeforeMessageID)
	if err := query.Validate(); err != nil {
		return domain.MessageHistoryPage{}, errors.New("load message history: invalid query")
	}

	var conversationID string
	var conversationStatus domain.ConversationStatus
	if err := repository.database.QueryRow(ctx, `
		SELECT id, status
		FROM conversations
		WHERE id = $1
		  AND customer_id = $2
	`, query.ConversationID, query.CustomerID).Scan(&conversationID, &conversationStatus); err != nil {
		return domain.MessageHistoryPage{}, mapDatabaseError("load history conversation", err)
	}

	var beforeCreatedAt *time.Time
	var beforeID string
	if query.BeforeMessageID != "" {
		var createdAt time.Time
		if err := repository.database.QueryRow(ctx, `
			SELECT created_at, id
			FROM messages
			WHERE conversation_id = $1
			  AND id = $2
		`, query.ConversationID, query.BeforeMessageID).Scan(&createdAt, &beforeID); err != nil {
			return domain.MessageHistoryPage{}, mapDatabaseError("load history cursor", err)
		}
		beforeCreatedAt = &createdAt
	}

	rows, err := repository.database.Query(ctx, `
		SELECT
			message.id,
			message.conversation_id,
			message.client_message_id,
			COALESCE(message.agent_run_id, ''),
			message.role,
			message.content,
			message.created_at,
			COALESCE(assistant_run.id, source_run.id, ''),
			COALESCE(assistant_run.status, source_run.status, ''),
			CASE WHEN message.role = 'assistant' THEN assistant_run.result ELSE NULL END,
			COALESCE(assistant_run.error_code, source_run.error_code, '')
		FROM messages AS message
		LEFT JOIN agent_runs AS source_run
		  ON source_run.source_message_id = message.id
		LEFT JOIN agent_runs AS assistant_run
		  ON assistant_run.id = message.agent_run_id
		WHERE message.conversation_id = $1
		  AND (
		        $2::timestamptz IS NULL
		        OR (message.created_at, message.id) < ($2, $3)
		      )
		ORDER BY message.created_at DESC, message.id DESC
		LIMIT $4
	`, query.ConversationID, beforeCreatedAt, beforeID, query.Limit+1)
	if err != nil {
		return domain.MessageHistoryPage{}, mapDatabaseError("load message history", err)
	}
	defer rows.Close()

	items := make([]domain.MessageHistoryItem, 0, query.Limit+1)
	for rows.Next() {
		var item domain.MessageHistoryItem
		var role string
		var runStatus string
		var rawResult []byte
		if err := rows.Scan(
			&item.Message.ID,
			&item.Message.ConversationID,
			&item.Message.ClientMessageID,
			&item.Message.AgentRunID,
			&role,
			&item.Message.Content,
			&item.Message.CreatedAt,
			&item.RunID,
			&runStatus,
			&rawResult,
			&item.RunErrorCode,
		); err != nil {
			return domain.MessageHistoryPage{}, mapDatabaseError("scan message history", err)
		}
		item.Message.Role = domain.MessageRole(role)
		item.RunStatus = domain.RunStatus(runStatus)
		if len(rawResult) > 0 {
			if err := json.Unmarshal(rawResult, &item.RunResult); err != nil {
				return domain.MessageHistoryPage{}, errors.New("load message history: invalid persisted result")
			}
		}
		if err := item.Validate(); err != nil {
			return domain.MessageHistoryPage{}, errors.New("load message history: invalid persisted item")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.MessageHistoryPage{}, mapDatabaseError("load message history", err)
	}

	page := domain.MessageHistoryPage{
		Items: items, ConversationStatus: conversationStatus,
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		page.NextBeforeMessageID = page.Items[len(page.Items)-1].Message.ID
	}
	for left, right := 0, len(page.Items)-1; left < right; left, right = left+1, right-1 {
		page.Items[left], page.Items[right] = page.Items[right], page.Items[left]
	}
	return page, nil
}

func loadPlanningHistory(
	ctx context.Context,
	transaction pgx.Tx,
	source domain.RunSource,
	limit int,
) ([]domain.Message, error) {
	if limit == 0 {
		return nil, nil
	}
	rows, err := transaction.Query(ctx, `
		SELECT id, conversation_id, client_message_id, COALESCE(agent_run_id, ''), role, content, created_at
		FROM (
			SELECT id, conversation_id, client_message_id, agent_run_id, role, content, created_at
			FROM messages
			WHERE conversation_id = $1
			  AND (created_at, id) < ($2, $3)
			ORDER BY created_at DESC, id DESC
			LIMIT $4
		) AS recent
		ORDER BY created_at, id
	`, source.Run.ConversationID, source.Message.CreatedAt, source.Message.ID, limit)
	if err != nil {
		return nil, mapDatabaseError("load planning history", err)
	}
	defer rows.Close()

	history := make([]domain.Message, 0, limit)
	for rows.Next() {
		var message domain.Message
		var role string
		if err := rows.Scan(
			&message.ID,
			&message.ConversationID,
			&message.ClientMessageID,
			&message.AgentRunID,
			&role,
			&message.Content,
			&message.CreatedAt,
		); err != nil {
			return nil, mapDatabaseError("scan planning history", err)
		}
		message.Role = domain.MessageRole(role)
		if err := message.Validate(); err != nil {
			return nil, errors.New("load planning history: invalid persisted message")
		}
		history = append(history, message)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("load planning history", err)
	}
	return history, nil
}
