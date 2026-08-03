package chat

import (
	"context"
	"errors"
	"strings"

	domain "agent-chat/internal/domain/chat"
)

const (
	defaultMessageHistoryPageSize = 50
	maxMessageHistoryPageSize     = 100
)

// HistoryRepository 定义客户会话历史分页读取 Port。
type HistoryRepository interface {
	LoadMessageHistory(context.Context, domain.MessageHistoryQuery) (domain.MessageHistoryPage, error)
}

// MessageHistoryRequest 是一次客户作用域内的历史分页请求。
type MessageHistoryRequest struct {
	CustomerID      string
	ConversationID  string
	BeforeMessageID string
	Limit           int
}

// HistoryService 读取可供客户端恢复的消息和 Run 结果。
type HistoryService struct {
	repository HistoryRepository
}

// NewHistoryService 创建客户会话历史服务。
func NewHistoryService(repository HistoryRepository) (*HistoryService, error) {
	if repository == nil {
		return nil, errors.New("message history repository is required")
	}
	return &HistoryService{repository: repository}, nil
}

// ReadMessageHistory 返回按时间升序排列的一页消息。
func (service *HistoryService) ReadMessageHistory(
	ctx context.Context,
	request MessageHistoryRequest,
) (domain.MessageHistoryPage, error) {
	request.CustomerID = strings.TrimSpace(request.CustomerID)
	request.ConversationID = strings.TrimSpace(request.ConversationID)
	request.BeforeMessageID = strings.TrimSpace(request.BeforeMessageID)
	if request.Limit == 0 {
		request.Limit = defaultMessageHistoryPageSize
	}
	query := domain.MessageHistoryQuery{
		CustomerID:      request.CustomerID,
		ConversationID:  request.ConversationID,
		BeforeMessageID: request.BeforeMessageID,
		Limit:           request.Limit,
	}
	if err := query.Validate(); err != nil || request.Limit > maxMessageHistoryPageSize {
		return domain.MessageHistoryPage{}, newFailure("invalid_message_history_request", false, err)
	}
	page, err := service.repository.LoadMessageHistory(ctx, query)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return domain.MessageHistoryPage{}, err
		case errors.Is(err, domain.ErrNotFound):
			return domain.MessageHistoryPage{}, newFailure("conversation_not_found", false, err)
		default:
			return domain.MessageHistoryPage{}, newFailure("load_message_history_failed", true, err)
		}
	}
	for index, item := range page.Items {
		if err := item.Validate(); err != nil || item.Message.ConversationID != request.ConversationID {
			return domain.MessageHistoryPage{}, newFailure("invalid_message_history_result", false, err)
		}
		if index > 0 {
			previous := page.Items[index-1].Message
			if item.Message.CreatedAt.Before(previous.CreatedAt) ||
				(item.Message.CreatedAt.Equal(previous.CreatedAt) && item.Message.ID <= previous.ID) {
				return domain.MessageHistoryPage{}, newFailure("invalid_message_history_result", false, nil)
			}
		}
	}
	if page.NextBeforeMessageID != "" {
		if len(page.NextBeforeMessageID) > maxScopedIDLength ||
			len(page.Items) == 0 ||
			page.NextBeforeMessageID != page.Items[0].Message.ID {
			return domain.MessageHistoryPage{}, newFailure("invalid_message_history_result", false, nil)
		}
	}
	return page, nil
}
