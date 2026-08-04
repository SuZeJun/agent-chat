package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	domain "agent-chat/internal/domain/chat"
)

const defaultConversationEventPageSize = 100

// RequestHandoffCommand 是客户转人工、摘要和首个审计事件的原子写入命令。
type RequestHandoffCommand struct {
	CustomerID      string
	ConversationID  string
	Reason          string
	EventID         string
	SystemMessageID string
	OccurredAt      time.Time
}

// CustomerHandoffMessageCommand 在人工阶段保存客户消息且不创建 Agent Run。
type CustomerHandoffMessageCommand struct {
	CustomerID string
	Message    domain.Message
	EventID    string
}

// TakeoverHandoffCommand 将等待会话分配给唯一客服。
type TakeoverHandoffCommand struct {
	AgentID         string
	ConversationID  string
	EventID         string
	SystemMessageID string
	OccurredAt      time.Time
}

// AgentHandoffMessageCommand 保存当前接管客服的人工回复。
type AgentHandoffMessageCommand struct {
	AgentID string
	Message domain.Message
	EventID string
}

// ResumeAICommand 由当前接管客服显式恢复 AI 接待。
type ResumeAICommand struct {
	AgentID         string
	ConversationID  string
	EventID         string
	SystemMessageID string
	OccurredAt      time.Time
}

// HandoffRepository 定义人工接管状态机及持久化事件所需能力。
type HandoffRepository interface {
	RequestHandoff(context.Context, RequestHandoffCommand) (domain.HandoffConversation, error)
	SaveHandoffCustomerMessage(context.Context, CustomerHandoffMessageCommand) (domain.Message, bool, error)
	ListHandoffConversations(context.Context, string) ([]domain.HandoffConversation, error)
	LoadHandoffConversation(context.Context, string, string) (domain.HandoffConversation, error)
	TakeoverHandoff(context.Context, TakeoverHandoffCommand) (domain.HandoffConversation, error)
	SaveHandoffAgentMessage(context.Context, AgentHandoffMessageCommand) (domain.Message, error)
	ResumeAI(context.Context, ResumeAICommand) (domain.HandoffConversation, error)
	LoadCustomerConversationEvents(context.Context, string, string, int, int) (domain.ConversationEventPage, error)
	LoadAgentConversationEvents(context.Context, string, string, int, int) (domain.ConversationEventPage, error)
}

// HandoffService 编排客户转人工与客服接管用例。
type HandoffService struct {
	repository  HandoffRepository
	idGenerator IDGenerator
	clock       Clock
}

// NewHandoffService 创建人工接管 Application Service。
func NewHandoffService(repository HandoffRepository, idGenerator IDGenerator, clock Clock) (*HandoffService, error) {
	if repository == nil {
		return nil, errors.New("handoff repository is required")
	}
	if idGenerator == nil {
		return nil, errors.New("handoff ID generator is required")
	}
	if clock == nil {
		return nil, errors.New("handoff clock is required")
	}
	return &HandoffService{repository: repository, idGenerator: idGenerator, clock: clock}, nil
}

// RequestHandoff 将当前客户会话原子切换到等待人工并持久化结构化摘要。
func (service *HandoffService) RequestHandoff(
	ctx context.Context,
	customerID string,
	conversationID string,
	reason string,
) (domain.HandoffConversation, error) {
	customerID, conversationID, reason = strings.TrimSpace(customerID), strings.TrimSpace(conversationID), strings.TrimSpace(reason)
	if err := validateHandoffScope(customerID, conversationID); err != nil {
		return domain.HandoffConversation{}, newFailure("invalid_handoff_request", false, err)
	}
	if utf8.RuneCountInString(reason) > 4_000 {
		return domain.HandoffConversation{}, newFailure("invalid_handoff_request", false, errors.New("handoff reason is too long"))
	}
	now := service.clock.Now().UTC()
	result, err := service.repository.RequestHandoff(ctx, RequestHandoffCommand{
		CustomerID: customerID, ConversationID: conversationID, Reason: reason,
		EventID: service.idGenerator.NewID("cevt_"), SystemMessageID: service.idGenerator.NewID("msg_"), OccurredAt: now,
	})
	return result, mapHandoffError(err)
}

// SendCustomerMessage 在等待或人工接管阶段保存客户消息，不启动 AI Run。
func (service *HandoffService) SendCustomerMessage(
	ctx context.Context,
	request Request,
) (domain.Message, bool, error) {
	request = normalizeRequest(request)
	if err := validateRequest(request); err != nil {
		return domain.Message{}, false, newFailure("invalid_handoff_message", false, err)
	}
	message := domain.Message{
		ID: service.idGenerator.NewID("msg_"), ConversationID: request.ConversationID,
		ClientMessageID: request.ClientMessageID, Role: domain.MessageRoleCustomer,
		Content: request.Content, CreatedAt: service.clock.Now().UTC(),
	}
	result, duplicate, err := service.repository.SaveHandoffCustomerMessage(ctx, CustomerHandoffMessageCommand{
		CustomerID: request.CustomerID, Message: message, EventID: service.idGenerator.NewID("cevt_"),
	})
	return result, duplicate, mapHandoffError(err)
}

// ListQueue 返回等待会话及当前客服已经接管的会话。
func (service *HandoffService) ListQueue(ctx context.Context, agentID string) ([]domain.HandoffConversation, error) {
	agentID = strings.TrimSpace(agentID)
	if err := validateSingleScope("agent ID", agentID); err != nil {
		return nil, newFailure("invalid_handoff_queue_request", false, err)
	}
	items, err := service.repository.ListHandoffConversations(ctx, agentID)
	return items, mapHandoffError(err)
}

// GetConversation 返回等待会话或当前客服负责会话的摘要、消息和审计事件。
func (service *HandoffService) GetConversation(ctx context.Context, agentID string, conversationID string) (domain.HandoffConversation, error) {
	agentID, conversationID = strings.TrimSpace(agentID), strings.TrimSpace(conversationID)
	if err := validateHandoffScope(agentID, conversationID); err != nil {
		return domain.HandoffConversation{}, newFailure("invalid_handoff_conversation_request", false, err)
	}
	result, err := service.repository.LoadHandoffConversation(ctx, agentID, conversationID)
	return result, mapHandoffError(err)
}

// Takeover 将 waiting_human 会话分配给当前客服。
func (service *HandoffService) Takeover(ctx context.Context, agentID string, conversationID string) (domain.HandoffConversation, error) {
	agentID, conversationID = strings.TrimSpace(agentID), strings.TrimSpace(conversationID)
	if err := validateHandoffScope(agentID, conversationID); err != nil {
		return domain.HandoffConversation{}, newFailure("invalid_handoff_takeover", false, err)
	}
	now := service.clock.Now().UTC()
	result, err := service.repository.TakeoverHandoff(ctx, TakeoverHandoffCommand{
		AgentID: agentID, ConversationID: conversationID,
		EventID: service.idGenerator.NewID("cevt_"), SystemMessageID: service.idGenerator.NewID("msg_"), OccurredAt: now,
	})
	return result, mapHandoffError(err)
}

// SendAgentMessage 保存当前接管客服的人工回复。
func (service *HandoffService) SendAgentMessage(ctx context.Context, agentID string, conversationID string, content string) (domain.Message, error) {
	agentID, conversationID, content = strings.TrimSpace(agentID), strings.TrimSpace(conversationID), strings.TrimSpace(content)
	if err := validateHandoffScope(agentID, conversationID); err != nil {
		return domain.Message{}, newFailure("invalid_agent_message", false, err)
	}
	if content == "" || utf8.RuneCountInString(content) > maxMessageRunes {
		return domain.Message{}, newFailure("invalid_agent_message", false, errors.New("agent message content is invalid"))
	}
	message := domain.Message{
		ID: service.idGenerator.NewID("msg_"), ConversationID: conversationID,
		Role: domain.MessageRoleAgent, Content: content, CreatedAt: service.clock.Now().UTC(),
	}
	result, err := service.repository.SaveHandoffAgentMessage(ctx, AgentHandoffMessageCommand{
		AgentID: agentID, Message: message, EventID: service.idGenerator.NewID("cevt_"),
	})
	return result, mapHandoffError(err)
}

// ResumeAI 仅允许当前接管客服显式恢复 AI 接待。
func (service *HandoffService) ResumeAI(ctx context.Context, agentID string, conversationID string) (domain.HandoffConversation, error) {
	agentID, conversationID = strings.TrimSpace(agentID), strings.TrimSpace(conversationID)
	if err := validateHandoffScope(agentID, conversationID); err != nil {
		return domain.HandoffConversation{}, newFailure("invalid_resume_ai_request", false, err)
	}
	now := service.clock.Now().UTC()
	result, err := service.repository.ResumeAI(ctx, ResumeAICommand{
		AgentID: agentID, ConversationID: conversationID,
		EventID: service.idGenerator.NewID("cevt_"), SystemMessageID: service.idGenerator.NewID("msg_"), OccurredAt: now,
	})
	return result, mapHandoffError(err)
}

// ReadCustomerEvents 在客户作用域内增量读取会话事件。
func (service *HandoffService) ReadCustomerEvents(ctx context.Context, customerID, conversationID string, after int) (domain.ConversationEventPage, error) {
	if err := validateEventRequest(customerID, conversationID, after); err != nil {
		return domain.ConversationEventPage{}, newFailure("invalid_conversation_event_request", false, err)
	}
	page, err := service.repository.LoadCustomerConversationEvents(ctx, strings.TrimSpace(customerID), strings.TrimSpace(conversationID), after, defaultConversationEventPageSize)
	return page, mapHandoffError(err)
}

// ReadAgentEvents 在客服授权范围内增量读取会话事件。
func (service *HandoffService) ReadAgentEvents(ctx context.Context, agentID, conversationID string, after int) (domain.ConversationEventPage, error) {
	if err := validateEventRequest(agentID, conversationID, after); err != nil {
		return domain.ConversationEventPage{}, newFailure("invalid_conversation_event_request", false, err)
	}
	page, err := service.repository.LoadAgentConversationEvents(ctx, strings.TrimSpace(agentID), strings.TrimSpace(conversationID), after, defaultConversationEventPageSize)
	return page, mapHandoffError(err)
}

func validateEventRequest(actorID, conversationID string, after int) error {
	if err := validateHandoffScope(strings.TrimSpace(actorID), strings.TrimSpace(conversationID)); err != nil {
		return err
	}
	if after < 0 {
		return errors.New("conversation event cursor must not be negative")
	}
	return nil
}

func validateHandoffScope(actorID string, conversationID string) error {
	if err := validateSingleScope("actor ID", actorID); err != nil {
		return err
	}
	return validateSingleScope("conversation ID", conversationID)
}

func validateSingleScope(name string, value string) error {
	if value == "" || len(value) > maxScopedIDLength {
		return fmt.Errorf("%s must be 1-%d characters", name, maxScopedIDLength)
	}
	return nil
}

func mapHandoffError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return newFailure("handoff_conversation_not_found", false, err)
	case errors.Is(err, domain.ErrInvalidState):
		return newFailure("handoff_state_conflict", false, err)
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return newFailure("handoff_message_id_conflict", false, err)
	case errors.Is(err, domain.ErrConflict):
		return newFailure("handoff_conflict", true, err)
	default:
		return newFailure("handoff_service_failed", true, err)
	}
}
