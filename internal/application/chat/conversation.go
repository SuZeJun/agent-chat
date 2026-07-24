package chat

import (
	"context"
	"errors"
	"strings"

	domain "agent-chat/internal/domain/chat"
)

// ConversationRepository 定义创建客户会话所需的持久化能力。
type ConversationRepository interface {
	CreateConversation(context.Context, domain.Conversation) error
}

// CreateConversationRequest 使用服务端身份绑定客户和知识库。
type CreateConversationRequest struct {
	CustomerID      string
	KnowledgeBaseID string
}

// CreateConversationResult 返回客户后续发送消息所需的会话信息。
type CreateConversationResult struct {
	ID              string
	KnowledgeBaseID string
	Status          domain.ConversationStatus
}

// ConversationService 创建 AI 接待会话。
type ConversationService struct {
	repository  ConversationRepository
	idGenerator IDGenerator
	clock       Clock
}

// NewConversationService 创建会话用例。
func NewConversationService(
	repository ConversationRepository,
	idGenerator IDGenerator,
	clock Clock,
) (*ConversationService, error) {
	if repository == nil {
		return nil, errors.New("conversation repository is required")
	}
	if idGenerator == nil {
		return nil, errors.New("conversation ID generator is required")
	}
	if clock == nil {
		return nil, errors.New("conversation clock is required")
	}
	return &ConversationService{
		repository:  repository,
		idGenerator: idGenerator,
		clock:       clock,
	}, nil
}

// Create 创建绑定当前客户和指定知识库的 AI 会话。
func (service *ConversationService) Create(
	ctx context.Context,
	request CreateConversationRequest,
) (CreateConversationResult, error) {
	request.CustomerID = strings.TrimSpace(request.CustomerID)
	request.KnowledgeBaseID = strings.TrimSpace(request.KnowledgeBaseID)
	now := service.clock.Now().UTC()
	conversation := domain.Conversation{
		ID:              service.idGenerator.NewID("conv_"),
		CustomerID:      request.CustomerID,
		KnowledgeBaseID: request.KnowledgeBaseID,
		Status:          domain.ConversationStatusAIActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := conversation.Validate(); err != nil {
		return CreateConversationResult{}, newFailure(
			"invalid_create_conversation",
			false,
			err,
		)
	}
	if err := service.repository.CreateConversation(ctx, conversation); err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return CreateConversationResult{}, err
		case errors.Is(err, domain.ErrNotFound):
			return CreateConversationResult{}, newFailure(
				"knowledge_base_not_found",
				false,
				err,
			)
		case errors.Is(err, domain.ErrConflict):
			return CreateConversationResult{}, newFailure(
				"create_conversation_conflict",
				true,
				err,
			)
		default:
			return CreateConversationResult{}, newFailure(
				"create_conversation_failed",
				true,
				err,
			)
		}
	}
	return CreateConversationResult{
		ID:              conversation.ID,
		KnowledgeBaseID: conversation.KnowledgeBaseID,
		Status:          conversation.Status,
	}, nil
}
