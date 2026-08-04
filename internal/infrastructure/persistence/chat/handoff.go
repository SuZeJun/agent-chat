package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"

	"github.com/jackc/pgx/v5"
)

// RequestHandoff 在会话锁内生成摘要、切换状态并写入系统消息和审计事件。
func (repository *Repository) RequestHandoff(
	ctx context.Context,
	command application.RequestHandoffCommand,
) (domain.HandoffConversation, error) {
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("request handoff", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status domain.ConversationStatus
	err = transaction.QueryRow(ctx, `
		SELECT status
		FROM conversations
		WHERE id = $1 AND customer_id = $2
		FOR UPDATE
	`, command.ConversationID, command.CustomerID).Scan(&status)
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("load conversation for handoff", err)
	}
	if status == domain.ConversationStatusWaitingHuman || status == domain.ConversationStatusHumanActive {
		// 幂等读取不能占着 FOR UPDATE 事务连接，否则单连接池会自锁。
		if err := transaction.Rollback(ctx); err != nil {
			return domain.HandoffConversation{}, mapDatabaseError("release handoff lock", err)
		}
		return repository.loadHandoffConversation(ctx, command.CustomerID, command.ConversationID, handoffScopeCustomer)
	}
	if status != domain.ConversationStatusAIActive {
		return domain.HandoffConversation{}, fmt.Errorf("request handoff: %w", domain.ErrInvalidState)
	}
	handoffContext, err := loadHandoffContext(ctx, transaction, command.ConversationID, command.Reason)
	if err != nil {
		return domain.HandoffConversation{}, err
	}
	summary, err := domain.BuildHandoffSummary(command.ConversationID, handoffContext, command.OccurredAt)
	if err != nil {
		return domain.HandoffConversation{}, fmt.Errorf("request handoff: %w", domain.ErrInvalidCommand)
	}
	if err := upsertHandoffSummary(ctx, transaction, summary); err != nil {
		return domain.HandoffConversation{}, err
	}
	systemMessage := domain.Message{
		ID: command.SystemMessageID, ConversationID: command.ConversationID,
		Role: domain.MessageRoleSystem, Content: "已为你转接人工支持，请稍候。", CreatedAt: command.OccurredAt,
	}
	if err := insertHandoffMessage(ctx, transaction, systemMessage); err != nil {
		return domain.HandoffConversation{}, err
	}
	commandTag, err := transaction.Exec(ctx, `
		UPDATE conversations
		SET status = 'waiting_human', assigned_agent_id = '',
		    last_message_at = GREATEST(COALESCE(last_message_at, $2), $2),
		    updated_at = GREATEST(updated_at, $2)
		WHERE id = $1 AND status = 'ai_active'
	`, command.ConversationID, command.OccurredAt)
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("mark conversation waiting human", err)
	}
	if commandTag.RowsAffected() != 1 {
		return domain.HandoffConversation{}, fmt.Errorf("mark conversation waiting human: %w", domain.ErrInvalidState)
	}
	if err := appendConversationEvent(ctx, transaction, domain.ConversationEvent{
		ID: command.EventID, ConversationID: command.ConversationID,
		Type:      domain.ConversationEventHandoffRequested,
		ActorType: domain.ConversationActorCustomer, ActorID: command.CustomerID,
		Payload:   map[string]any{"status": string(domain.ConversationStatusWaitingHuman), "systemMessageId": systemMessage.ID},
		CreatedAt: command.OccurredAt,
	}); err != nil {
		return domain.HandoffConversation{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("request handoff", err)
	}
	return repository.loadHandoffConversation(ctx, command.CustomerID, command.ConversationID, handoffScopeCustomer)
}

// SaveHandoffCustomerMessage 保存人工阶段客户消息，同一 client_message_id 只允许相同内容重放。
func (repository *Repository) SaveHandoffCustomerMessage(
	ctx context.Context,
	command application.CustomerHandoffMessageCommand,
) (domain.Message, bool, error) {
	if err := command.Message.Validate(); err != nil || command.Message.Role != domain.MessageRoleCustomer {
		return domain.Message{}, false, fmt.Errorf("save handoff customer message: %w", domain.ErrInvalidCommand)
	}
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Message{}, false, mapDatabaseError("save handoff customer message", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status domain.ConversationStatus
	err = transaction.QueryRow(ctx, `
		SELECT status FROM conversations
		WHERE id = $1 AND customer_id = $2
		FOR UPDATE
	`, command.Message.ConversationID, command.CustomerID).Scan(&status)
	if err != nil {
		return domain.Message{}, false, mapDatabaseError("load conversation for handoff message", err)
	}
	existing, found, err := loadExistingCustomerMessage(ctx, transaction, command.Message.ConversationID, command.Message.ClientMessageID)
	if err != nil {
		return domain.Message{}, false, err
	}
	if found {
		if existing.Content != command.Message.Content {
			return domain.Message{}, false, fmt.Errorf("save handoff customer message: %w", domain.ErrIdempotencyConflict)
		}
		return existing, true, nil
	}
	if status != domain.ConversationStatusWaitingHuman && status != domain.ConversationStatusHumanActive {
		return domain.Message{}, false, fmt.Errorf("save handoff customer message: %w", domain.ErrInvalidState)
	}
	if err := insertHandoffMessage(ctx, transaction, command.Message); err != nil {
		return domain.Message{}, false, err
	}
	if err := appendConversationEvent(ctx, transaction, domain.ConversationEvent{
		ID: command.EventID, ConversationID: command.Message.ConversationID,
		Type:      domain.ConversationEventCustomerMessage,
		ActorType: domain.ConversationActorCustomer, ActorID: command.CustomerID,
		Payload: messageEventPayload(command.Message), CreatedAt: command.Message.CreatedAt,
	}); err != nil {
		return domain.Message{}, false, err
	}
	if err := updateConversationActivity(ctx, transaction, command.Message.ConversationID, command.Message.CreatedAt); err != nil {
		return domain.Message{}, false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.Message{}, false, mapDatabaseError("save handoff customer message", err)
	}
	return command.Message, false, nil
}

// ListHandoffConversations 返回公共等待队列和当前客服负责的会话。
func (repository *Repository) ListHandoffConversations(ctx context.Context, agentID string) ([]domain.HandoffConversation, error) {
	rows, err := repository.database.Query(ctx, `
		SELECT conversation.id
		FROM conversations AS conversation
		JOIN handoff_summaries AS summary ON summary.conversation_id = conversation.id
		WHERE conversation.status = 'waiting_human'
		   OR (conversation.status = 'human_active' AND conversation.assigned_agent_id = $1)
		ORDER BY CASE WHEN conversation.status = 'waiting_human' THEN 0 ELSE 1 END,
		         conversation.updated_at, conversation.id
	`, agentID)
	if err != nil {
		return nil, mapDatabaseError("list handoff conversations", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, mapDatabaseError("scan handoff conversation", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("list handoff conversations", err)
	}
	items := make([]domain.HandoffConversation, 0, len(ids))
	for _, id := range ids {
		item, err := repository.loadHandoffConversation(ctx, agentID, id, handoffScopeAgent)
		if err != nil {
			return nil, err
		}
		item.Messages = nil
		item.Events = nil
		items = append(items, item)
	}
	return items, nil
}

// LoadHandoffConversation 只允许客服读取等待队列或自己已经接管的会话。
func (repository *Repository) LoadHandoffConversation(ctx context.Context, agentID string, conversationID string) (domain.HandoffConversation, error) {
	return repository.loadHandoffConversation(ctx, agentID, conversationID, handoffScopeAgent)
}

// TakeoverHandoff 原子认领 waiting_human 会话，防止两个客服同时接管。
func (repository *Repository) TakeoverHandoff(
	ctx context.Context,
	command application.TakeoverHandoffCommand,
) (domain.HandoffConversation, error) {
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("take over handoff", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status domain.ConversationStatus
	var assignedAgentID string
	err = transaction.QueryRow(ctx, `
		SELECT status, assigned_agent_id FROM conversations
		WHERE id = $1
		FOR UPDATE
	`, command.ConversationID).Scan(&status, &assignedAgentID)
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("load conversation for takeover", err)
	}
	if status == domain.ConversationStatusHumanActive && assignedAgentID == command.AgentID {
		// 幂等读取不能占着 FOR UPDATE 事务连接，否则单连接池会自锁。
		if err := transaction.Rollback(ctx); err != nil {
			return domain.HandoffConversation{}, mapDatabaseError("release takeover lock", err)
		}
		return repository.loadHandoffConversation(ctx, command.AgentID, command.ConversationID, handoffScopeAgent)
	}
	if status != domain.ConversationStatusWaitingHuman {
		return domain.HandoffConversation{}, fmt.Errorf("take over handoff: %w", domain.ErrInvalidState)
	}
	systemMessage := domain.Message{
		ID: command.SystemMessageID, ConversationID: command.ConversationID,
		Role: domain.MessageRoleSystem, Content: "人工客服已接入会话。", CreatedAt: command.OccurredAt,
	}
	if err := insertHandoffMessage(ctx, transaction, systemMessage); err != nil {
		return domain.HandoffConversation{}, err
	}
	commandTag, err := transaction.Exec(ctx, `
		UPDATE conversations
		SET status = 'human_active', assigned_agent_id = $2,
		    last_message_at = GREATEST(COALESCE(last_message_at, $3), $3),
		    updated_at = GREATEST(updated_at, $3)
		WHERE id = $1 AND status = 'waiting_human'
	`, command.ConversationID, command.AgentID, command.OccurredAt)
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("assign handoff conversation", err)
	}
	if commandTag.RowsAffected() != 1 {
		return domain.HandoffConversation{}, fmt.Errorf("assign handoff conversation: %w", domain.ErrInvalidState)
	}
	if err := appendConversationEvent(ctx, transaction, domain.ConversationEvent{
		ID: command.EventID, ConversationID: command.ConversationID,
		Type:      domain.ConversationEventTakenOver,
		ActorType: domain.ConversationActorAgent, ActorID: command.AgentID,
		Payload:   map[string]any{"status": string(domain.ConversationStatusHumanActive), "systemMessageId": systemMessage.ID},
		CreatedAt: command.OccurredAt,
	}); err != nil {
		return domain.HandoffConversation{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("take over handoff", err)
	}
	return repository.loadHandoffConversation(ctx, command.AgentID, command.ConversationID, handoffScopeAgent)
}

// SaveHandoffAgentMessage 仅允许当前接管客服发送人工回复。
func (repository *Repository) SaveHandoffAgentMessage(
	ctx context.Context,
	command application.AgentHandoffMessageCommand,
) (domain.Message, error) {
	if err := command.Message.Validate(); err != nil || command.Message.Role != domain.MessageRoleAgent {
		return domain.Message{}, fmt.Errorf("save handoff agent message: %w", domain.ErrInvalidCommand)
	}
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Message{}, mapDatabaseError("save handoff agent message", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status domain.ConversationStatus
	var assignedAgentID string
	err = transaction.QueryRow(ctx, `
		SELECT status, assigned_agent_id FROM conversations
		WHERE id = $1
		FOR UPDATE
	`, command.Message.ConversationID).Scan(&status, &assignedAgentID)
	if err != nil {
		return domain.Message{}, mapDatabaseError("load conversation for agent message", err)
	}
	if status != domain.ConversationStatusHumanActive || assignedAgentID != command.AgentID {
		return domain.Message{}, fmt.Errorf("save handoff agent message: %w", domain.ErrInvalidState)
	}
	if err := insertHandoffMessage(ctx, transaction, command.Message); err != nil {
		return domain.Message{}, err
	}
	if err := appendConversationEvent(ctx, transaction, domain.ConversationEvent{
		ID: command.EventID, ConversationID: command.Message.ConversationID,
		Type:      domain.ConversationEventAgentMessage,
		ActorType: domain.ConversationActorAgent, ActorID: command.AgentID,
		Payload: messageEventPayload(command.Message), CreatedAt: command.Message.CreatedAt,
	}); err != nil {
		return domain.Message{}, err
	}
	if err := updateConversationActivity(ctx, transaction, command.Message.ConversationID, command.Message.CreatedAt); err != nil {
		return domain.Message{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.Message{}, mapDatabaseError("save handoff agent message", err)
	}
	return command.Message, nil
}

// ResumeAI 由当前接管客服原子恢复 AI 状态并清除客服分配。
func (repository *Repository) ResumeAI(ctx context.Context, command application.ResumeAICommand) (domain.HandoffConversation, error) {
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("resume AI", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status domain.ConversationStatus
	var assignedAgentID string
	err = transaction.QueryRow(ctx, `
		SELECT status, assigned_agent_id FROM conversations
		WHERE id = $1
		FOR UPDATE
	`, command.ConversationID).Scan(&status, &assignedAgentID)
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("load conversation for AI resume", err)
	}
	if status != domain.ConversationStatusHumanActive || assignedAgentID != command.AgentID {
		return domain.HandoffConversation{}, fmt.Errorf("resume AI: %w", domain.ErrInvalidState)
	}
	systemMessage := domain.Message{
		ID: command.SystemMessageID, ConversationID: command.ConversationID,
		Role: domain.MessageRoleSystem, Content: "客服已恢复 AI 接待，你的下一条消息将由 AI 处理。", CreatedAt: command.OccurredAt,
	}
	if err := insertHandoffMessage(ctx, transaction, systemMessage); err != nil {
		return domain.HandoffConversation{}, err
	}
	commandTag, err := transaction.Exec(ctx, `
		UPDATE conversations
		SET status = 'ai_active', assigned_agent_id = '',
		    last_message_at = GREATEST(COALESCE(last_message_at, $2), $2),
		    updated_at = GREATEST(updated_at, $2)
		WHERE id = $1 AND status = 'human_active' AND assigned_agent_id = $3
	`, command.ConversationID, command.OccurredAt, command.AgentID)
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("mark conversation AI active", err)
	}
	if commandTag.RowsAffected() != 1 {
		return domain.HandoffConversation{}, fmt.Errorf("mark conversation AI active: %w", domain.ErrInvalidState)
	}
	if err := appendConversationEvent(ctx, transaction, domain.ConversationEvent{
		ID: command.EventID, ConversationID: command.ConversationID,
		Type:      domain.ConversationEventAIResumed,
		ActorType: domain.ConversationActorAgent, ActorID: command.AgentID,
		Payload:   map[string]any{"status": string(domain.ConversationStatusAIActive), "systemMessageId": systemMessage.ID},
		CreatedAt: command.OccurredAt,
	}); err != nil {
		return domain.HandoffConversation{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("resume AI", err)
	}
	return repository.loadHandoffConversation(ctx, "", command.ConversationID, handoffScopeInternal)
}

// LoadCustomerConversationEvents 在客户归属范围内读取持久化会话事件。
func (repository *Repository) LoadCustomerConversationEvents(ctx context.Context, customerID, conversationID string, after, limit int) (domain.ConversationEventPage, error) {
	return repository.loadConversationEvents(ctx, customerID, conversationID, after, limit, handoffScopeCustomer)
}

// LoadAgentConversationEvents 在等待队列或当前客服负责范围内读取会话事件。
func (repository *Repository) LoadAgentConversationEvents(ctx context.Context, agentID, conversationID string, after, limit int) (domain.ConversationEventPage, error) {
	return repository.loadConversationEvents(ctx, agentID, conversationID, after, limit, handoffScopeAgent)
}

type handoffScope int

const (
	handoffScopeCustomer handoffScope = iota
	handoffScopeAgent
	handoffScopeInternal
)

func (repository *Repository) loadHandoffConversation(ctx context.Context, actorID, conversationID string, scope handoffScope) (domain.HandoffConversation, error) {
	var result domain.HandoffConversation
	var status domain.ConversationStatus
	var factsJSON, unresolvedJSON, risksJSON, citationsJSON, toolsJSON []byte
	query := `
		SELECT conversation.id, conversation.customer_id, conversation.knowledge_base_id,
		       conversation.status, conversation.created_at, conversation.updated_at,
		       conversation.assigned_agent_id, conversation.last_message_at,
		       summary.customer_request, summary.confirmed_facts, summary.unresolved_questions,
		       summary.risk_signals, summary.citations, summary.tool_calls,
		       summary.recommended_action, summary.created_at, summary.updated_at
		FROM conversations AS conversation
		JOIN handoff_summaries AS summary ON summary.conversation_id = conversation.id
		WHERE conversation.id = $1`
	args := []any{conversationID}
	switch scope {
	case handoffScopeCustomer:
		query += ` AND conversation.customer_id = $2`
		args = append(args, actorID)
	case handoffScopeAgent:
		query += ` AND (conversation.status = 'waiting_human'
		              OR (conversation.status = 'human_active' AND conversation.assigned_agent_id = $2))`
		args = append(args, actorID)
	}
	err := repository.database.QueryRow(ctx, query, args...).Scan(
		&result.Conversation.ID, &result.Conversation.CustomerID, &result.Conversation.KnowledgeBaseID,
		&status, &result.Conversation.CreatedAt, &result.Conversation.UpdatedAt,
		&result.AssignedAgentID, &result.LastMessageAt,
		&result.Summary.CustomerRequest, &factsJSON, &unresolvedJSON,
		&risksJSON, &citationsJSON, &toolsJSON,
		&result.Summary.RecommendedAction, &result.Summary.CreatedAt, &result.Summary.UpdatedAt,
	)
	if err != nil {
		return domain.HandoffConversation{}, mapDatabaseError("load handoff conversation", err)
	}
	result.Conversation.Status = status
	result.Summary.ConversationID = result.Conversation.ID
	if err := unmarshalSummaryArrays(&result.Summary, factsJSON, unresolvedJSON, risksJSON, citationsJSON, toolsJSON); err != nil {
		return domain.HandoffConversation{}, err
	}
	result.Messages, err = repository.loadHandoffMessages(ctx, conversationID)
	if err != nil {
		return domain.HandoffConversation{}, err
	}
	page, err := repository.loadConversationEvents(ctx, actorID, conversationID, 0, 100, scope)
	if err != nil {
		return domain.HandoffConversation{}, err
	}
	result.Events = page.Events
	return result, nil
}

func (repository *Repository) loadHandoffMessages(ctx context.Context, conversationID string) ([]domain.Message, error) {
	rows, err := repository.database.Query(ctx, `
		SELECT id, conversation_id, client_message_id, COALESCE(agent_run_id, ''), role, content, created_at
		FROM (
			SELECT id, conversation_id, client_message_id, agent_run_id, role, content, created_at
			FROM messages WHERE conversation_id = $1
			ORDER BY created_at DESC, id DESC LIMIT 100
		) AS recent
		ORDER BY created_at, id
	`, conversationID)
	if err != nil {
		return nil, mapDatabaseError("load handoff messages", err)
	}
	defer rows.Close()
	messages := make([]domain.Message, 0)
	for rows.Next() {
		var message domain.Message
		var role string
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.ClientMessageID,
			&message.AgentRunID, &role, &message.Content, &message.CreatedAt); err != nil {
			return nil, mapDatabaseError("scan handoff message", err)
		}
		message.Role = domain.MessageRole(role)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("load handoff messages", err)
	}
	return messages, nil
}

func (repository *Repository) loadConversationEvents(ctx context.Context, actorID, conversationID string, after, limit int, scope handoffScope) (domain.ConversationEventPage, error) {
	var page domain.ConversationEventPage
	var status domain.ConversationStatus
	query := `SELECT status, assigned_agent_id FROM conversations WHERE id = $1`
	args := []any{conversationID}
	switch scope {
	case handoffScopeCustomer:
		query += ` AND customer_id = $2`
		args = append(args, actorID)
	case handoffScopeAgent:
		query += ` AND (status = 'waiting_human' OR (status = 'human_active' AND assigned_agent_id = $2))`
		args = append(args, actorID)
	}
	if err := repository.database.QueryRow(ctx, query, args...).Scan(&status, &page.AssignedAgentID); err != nil {
		return domain.ConversationEventPage{}, mapDatabaseError("load conversation event scope", err)
	}
	page.ConversationID, page.Status = conversationID, status
	rows, err := repository.database.Query(ctx, `
		SELECT id, sequence, event_type, actor_type, actor_id, payload, created_at
		FROM conversation_events
		WHERE conversation_id = $1 AND sequence > $2
		ORDER BY sequence LIMIT $3
	`, conversationID, after, limit)
	if err != nil {
		return domain.ConversationEventPage{}, mapDatabaseError("load conversation events", err)
	}
	defer rows.Close()
	page.Events = make([]domain.ConversationEvent, 0)
	for rows.Next() {
		var event domain.ConversationEvent
		var payload []byte
		if err := rows.Scan(&event.ID, &event.Sequence, &event.Type, &event.ActorType, &event.ActorID, &payload, &event.CreatedAt); err != nil {
			return domain.ConversationEventPage{}, mapDatabaseError("scan conversation event", err)
		}
		event.ConversationID = conversationID
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return domain.ConversationEventPage{}, errors.New("load conversation events: invalid persisted payload")
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return domain.ConversationEventPage{}, mapDatabaseError("load conversation events", err)
	}
	return page, nil
}

func loadHandoffContext(ctx context.Context, transaction pgx.Tx, conversationID, reason string) (domain.HandoffContext, error) {
	contextValue := domain.HandoffContext{Reason: reason, Messages: make([]domain.Message, 0)}
	rows, err := transaction.Query(ctx, `
		SELECT id, conversation_id, client_message_id, COALESCE(agent_run_id, ''), role, content, created_at
		FROM (
			SELECT id, conversation_id, client_message_id, agent_run_id, role, content, created_at
			FROM messages WHERE conversation_id = $1
			ORDER BY created_at DESC, id DESC LIMIT 50
		) AS recent ORDER BY created_at, id
	`, conversationID)
	if err != nil {
		return domain.HandoffContext{}, mapDatabaseError("load handoff context messages", err)
	}
	for rows.Next() {
		var message domain.Message
		var role string
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.ClientMessageID,
			&message.AgentRunID, &role, &message.Content, &message.CreatedAt); err != nil {
			rows.Close()
			return domain.HandoffContext{}, mapDatabaseError("scan handoff context message", err)
		}
		message.Role = domain.MessageRole(role)
		contextValue.Messages = append(contextValue.Messages, message)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.HandoffContext{}, mapDatabaseError("load handoff context messages", err)
	}
	resultRows, err := transaction.Query(ctx, `
		SELECT result FROM agent_runs
		WHERE conversation_id = $1 AND status = 'completed'
		ORDER BY completed_at DESC NULLS LAST, id DESC LIMIT 20
	`, conversationID)
	if err != nil {
		return domain.HandoffContext{}, mapDatabaseError("load handoff run results", err)
	}
	defer resultRows.Close()
	for resultRows.Next() {
		var raw []byte
		if err := resultRows.Scan(&raw); err != nil {
			return domain.HandoffContext{}, mapDatabaseError("scan handoff run result", err)
		}
		if err := appendHandoffResult(&contextValue, raw); err != nil {
			return domain.HandoffContext{}, err
		}
	}
	if err := resultRows.Err(); err != nil {
		return domain.HandoffContext{}, mapDatabaseError("load handoff run results", err)
	}
	return contextValue, nil
}

func appendHandoffResult(contextValue *domain.HandoffContext, raw []byte) error {
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return errors.New("load handoff run result: invalid persisted result")
	}
	if citations, ok := result["citations"].([]any); ok {
		for _, value := range citations {
			item, ok := value.(map[string]any)
			if !ok {
				continue
			}
			contextValue.Citations = append(contextValue.Citations, domain.HandoffCitation{
				SourceID: stringField(item, "sourceId"), Title: stringField(item, "title"),
				Excerpt: stringField(item, "excerpt"), DocumentID: stringField(item, "documentId"),
				VersionID: stringField(item, "versionId"),
			})
		}
	}
	if calls, ok := result["toolCalls"].([]any); ok {
		for _, value := range calls {
			item, ok := value.(map[string]any)
			if !ok {
				continue
			}
			contextValue.ToolCalls = append(contextValue.ToolCalls, domain.HandoffToolCall{
				Name: stringField(item, "name"), Status: stringField(item, "status"), ErrorCode: stringField(item, "errorCode"),
			})
		}
	}
	return nil
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return strings.TrimSpace(value)
}

func upsertHandoffSummary(ctx context.Context, transaction pgx.Tx, summary domain.HandoffSummary) error {
	facts, _ := json.Marshal(summary.ConfirmedFacts)
	unresolved, _ := json.Marshal(summary.UnresolvedQuestions)
	risks, _ := json.Marshal(summary.RiskSignals)
	citations, _ := json.Marshal(summary.Citations)
	tools, _ := json.Marshal(summary.ToolCalls)
	_, err := transaction.Exec(ctx, `
		INSERT INTO handoff_summaries (
			conversation_id, customer_request, confirmed_facts, unresolved_questions,
			risk_signals, citations, tool_calls, recommended_action, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (conversation_id) DO UPDATE SET
			customer_request = EXCLUDED.customer_request,
			confirmed_facts = EXCLUDED.confirmed_facts,
			unresolved_questions = EXCLUDED.unresolved_questions,
			risk_signals = EXCLUDED.risk_signals,
			citations = EXCLUDED.citations,
			tool_calls = EXCLUDED.tool_calls,
			recommended_action = EXCLUDED.recommended_action,
			updated_at = EXCLUDED.updated_at
	`, summary.ConversationID, summary.CustomerRequest, facts, unresolved, risks,
		citations, tools, summary.RecommendedAction, summary.CreatedAt, summary.UpdatedAt)
	return mapDatabaseError("save handoff summary", err)
}

func unmarshalSummaryArrays(summary *domain.HandoffSummary, facts, unresolved, risks, citations, tools []byte) error {
	for _, item := range []struct {
		raw    []byte
		target any
	}{
		{facts, &summary.ConfirmedFacts}, {unresolved, &summary.UnresolvedQuestions},
		{risks, &summary.RiskSignals}, {citations, &summary.Citations}, {tools, &summary.ToolCalls},
	} {
		if err := json.Unmarshal(item.raw, item.target); err != nil {
			return errors.New("load handoff summary: invalid persisted JSON")
		}
	}
	return summary.Validate()
}

func insertHandoffMessage(ctx context.Context, transaction pgx.Tx, message domain.Message) error {
	if err := message.Validate(); err != nil {
		return fmt.Errorf("insert handoff message: %w", domain.ErrInvalidCommand)
	}
	_, err := transaction.Exec(ctx, `
		INSERT INTO messages (id, conversation_id, client_message_id, role, content, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, message.ID, message.ConversationID, message.ClientMessageID, message.Role, message.Content, message.CreatedAt)
	return mapDatabaseError("insert handoff message", err)
}

func loadExistingCustomerMessage(ctx context.Context, transaction pgx.Tx, conversationID, clientMessageID string) (domain.Message, bool, error) {
	var message domain.Message
	var role string
	err := transaction.QueryRow(ctx, `
		SELECT id, conversation_id, client_message_id, role, content, created_at
		FROM messages WHERE conversation_id = $1 AND client_message_id = $2
	`, conversationID, clientMessageID).Scan(&message.ID, &message.ConversationID, &message.ClientMessageID, &role, &message.Content, &message.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, false, nil
	}
	if err != nil {
		return domain.Message{}, false, mapDatabaseError("load existing handoff message", err)
	}
	message.Role = domain.MessageRole(role)
	return message, true, nil
}

func appendConversationEvent(ctx context.Context, transaction pgx.Tx, event domain.ConversationEvent) error {
	if err := transaction.QueryRow(ctx, `
		SELECT COALESCE(max(sequence), 0) + 1 FROM conversation_events WHERE conversation_id = $1
	`, event.ConversationID).Scan(&event.Sequence); err != nil {
		return mapDatabaseError("allocate conversation event sequence", err)
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("append conversation event: %w", domain.ErrInvalidCommand)
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return errors.New("append conversation event: encode payload")
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO conversation_events (id, conversation_id, sequence, event_type, actor_type, actor_id, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, event.ID, event.ConversationID, event.Sequence, event.Type, event.ActorType, event.ActorID, payload, event.CreatedAt)
	return mapDatabaseError("append conversation event", err)
}

func updateConversationActivity(ctx context.Context, transaction pgx.Tx, conversationID string, occurredAt time.Time) error {
	_, err := transaction.Exec(ctx, `
		UPDATE conversations SET
			last_message_at = GREATEST(COALESCE(last_message_at, $2), $2),
			updated_at = GREATEST(updated_at, $2)
		WHERE id = $1
	`, conversationID, occurredAt)
	return mapDatabaseError("update handoff conversation activity", err)
}

func messageEventPayload(message domain.Message) map[string]any {
	return map[string]any{"messageId": message.ID, "role": string(message.Role), "content": message.Content}
}
