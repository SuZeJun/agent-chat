package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	domain "agent-chat/internal/domain/chat"

	"github.com/google/uuid"
)

const (
	maxScopedIDLength        = 64
	maxClientMessageIDLength = 100
	maxMessageRunes          = 16_000
)

// IDGenerator 为一次发送生成消息、Run、事件和 Job 的唯一标识。
type IDGenerator interface {
	NewID(prefix string) string
}

// Clock 为用例提供可测试的业务时间。
type Clock interface {
	Now() time.Time
}

// UUIDGenerator 使用随机 UUID 生成带业务前缀的标识。
type UUIDGenerator struct{}

// NewID 生成不含连字符、且满足数据库长度约束的标识。
func (UUIDGenerator) NewID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// SystemClock 使用系统 UTC 时间。
type SystemClock struct{}

// Now 返回当前 UTC 时间。
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

// Request 是通过服务端鉴权上下文绑定客户后的发送消息请求。
type Request struct {
	CustomerID      string
	ConversationID  string
	ClientMessageID string
	Content         string
}

// Result 返回客户端继续订阅运行事件所需的稳定标识。
type Result struct {
	MessageID string
	RunID     string
	RunStatus domain.RunStatus
	Duplicate bool
}

// Failure 是可安全映射到 API 的发送消息错误。
type Failure struct {
	Code         string
	RetryAllowed bool
	cause        error
}

// Error 返回不包含消息内容、数据库细节或客户标识的稳定错误码。
func (failure *Failure) Error() string {
	return failure.Code
}

// Unwrap 仅用于进程内错误分类，不应记录底层 cause。
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// CanRetry 表示调用方是否可以安全重试本次请求。
func (failure *Failure) CanRetry() bool {
	return failure.RetryAllowed
}

// Service 负责规范化请求并构造原子聊天启动提交。
type Service struct {
	repository  domain.Repository
	idGenerator IDGenerator
	clock       Clock
}

// NewService 创建发送消息 Application Service。
func NewService(
	repository domain.Repository,
	idGenerator IDGenerator,
	clock Clock,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("chat repository is required")
	}
	if idGenerator == nil {
		return nil, errors.New("chat ID generator is required")
	}
	if clock == nil {
		return nil, errors.New("chat clock is required")
	}
	return &Service{
		repository:  repository,
		idGenerator: idGenerator,
		clock:       clock,
	}, nil
}

// SendMessage 原子保存客户消息并创建 pending Agent Run、首事件和持久化 Job。
func (service *Service) SendMessage(
	ctx context.Context,
	request Request,
) (Result, error) {
	request = normalizeRequest(request)
	if err := validateRequest(request); err != nil {
		return Result{}, newFailure("invalid_send_message", false, err)
	}

	now := service.clock.Now().UTC()
	submission := domain.StartRunSubmission{
		CustomerID: request.CustomerID,
		Message: domain.Message{
			ID:              service.idGenerator.NewID("msg_"),
			ConversationID:  request.ConversationID,
			ClientMessageID: request.ClientMessageID,
			Role:            domain.MessageRoleCustomer,
			Content:         request.Content,
			CreatedAt:       now,
		},
		Run: domain.AgentRun{
			ID:             service.idGenerator.NewID("run_"),
			ConversationID: request.ConversationID,
			Status:         domain.RunStatusPending,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		Event: domain.RunEvent{
			ID:        service.idGenerator.NewID("evt_"),
			Sequence:  1,
			Type:      domain.EventTypeRunStatus,
			Payload:   map[string]any{"status": string(domain.RunStatusPending)},
			CreatedAt: now,
		},
		JobID: service.idGenerator.NewID("job_"),
	}
	submission.Run.SourceMessageID = submission.Message.ID
	submission.Event.RunID = submission.Run.ID
	if err := submission.Validate(); err != nil {
		return Result{}, newFailure("invalid_chat_submission", false, err)
	}

	result, err := service.repository.StartRun(ctx, submission)
	if err != nil {
		return Result{}, mapRepositoryError(err)
	}
	if err := validateResult(request, result); err != nil {
		return Result{}, newFailure("invalid_chat_result", false, err)
	}
	return Result{
		MessageID: result.Message.ID,
		RunID:     result.Run.ID,
		RunStatus: result.Run.Status,
		Duplicate: result.Duplicate,
	}, nil
}

func normalizeRequest(request Request) Request {
	request.CustomerID = strings.TrimSpace(request.CustomerID)
	request.ConversationID = strings.TrimSpace(request.ConversationID)
	request.ClientMessageID = strings.TrimSpace(request.ClientMessageID)
	request.Content = strings.TrimSpace(request.Content)
	return request
}

func validateRequest(request Request) error {
	for name, value := range map[string]string{
		"customer ID":     request.CustomerID,
		"conversation ID": request.ConversationID,
	} {
		if value == "" || len(value) > maxScopedIDLength {
			return fmt.Errorf("%s must be 1-%d characters", name, maxScopedIDLength)
		}
	}
	if request.ClientMessageID == "" ||
		len(request.ClientMessageID) > maxClientMessageIDLength {
		return fmt.Errorf(
			"client message ID must be 1-%d characters",
			maxClientMessageIDLength,
		)
	}
	if request.Content == "" || utf8.RuneCountInString(request.Content) > maxMessageRunes {
		return fmt.Errorf("message content must be 1-%d characters", maxMessageRunes)
	}
	return nil
}

func validateResult(request Request, result domain.StartRunResult) error {
	if err := result.Message.Validate(); err != nil {
		return err
	}
	if err := result.Run.ValidateSnapshot(); err != nil {
		return err
	}
	if result.Message.ConversationID != request.ConversationID ||
		result.Message.ClientMessageID != request.ClientMessageID ||
		result.Message.Content != request.Content ||
		result.Run.ConversationID != request.ConversationID ||
		result.Run.SourceMessageID != result.Message.ID {
		return errors.New("repository result does not match request")
	}
	return nil
}

func mapRepositoryError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, domain.ErrNotFound):
		return newFailure("conversation_not_found", false, err)
	case errors.Is(err, domain.ErrInvalidState):
		return newFailure("conversation_not_ai_active", false, err)
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return newFailure("client_message_id_conflict", false, err)
	case errors.Is(err, domain.ErrConflict):
		return newFailure("send_message_conflict", true, err)
	default:
		return newFailure("send_message_failed", true, err)
	}
}

func newFailure(code string, retryAllowed bool, cause error) error {
	return &Failure{
		Code:         code,
		RetryAllowed: retryAllowed,
		cause:        cause,
	}
}
