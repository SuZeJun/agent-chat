package ticketapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "agent-chat/internal/domain/ticket"
)

const maxScopedIDLength = 64

// IDGenerator 生成带前缀的实体标识。
type IDGenerator interface {
	NewID(prefix string) string
}

// Clock 提供可注入的当前时间。
type Clock interface {
	Now() time.Time
}

// Failure 是可安全跨 Application 边界返回的稳定失败。
type Failure struct {
	Code         string
	RetryAllowed bool
	cause        error
}

// Error 返回稳定错误码。
func (failure *Failure) Error() string { return failure.Code }

// Unwrap 仅供进程内错误判断使用。
func (failure *Failure) Unwrap() error { return failure.cause }

// CanRetry 返回调用方是否可以安全重试。
func (failure *Failure) CanRetry() bool { return failure.RetryAllowed }

func newFailure(code string, retryAllowed bool, cause error) error {
	return &Failure{Code: code, RetryAllowed: retryAllowed, cause: cause}
}

// Decision 是一次确认或取消的结果。
type Decision struct {
	Approval domain.Approval
	Ticket   *domain.Ticket
}

// Service 编排工单草稿的确认与取消。
type Service struct {
	repository  domain.Repository
	idGenerator IDGenerator
	clock       Clock
}

// NewService 创建工单审批 Application Service。
func NewService(
	repository domain.Repository,
	idGenerator IDGenerator,
	clock Clock,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("ticket repository is required")
	}
	if idGenerator == nil {
		return nil, errors.New("ticket ID generator is required")
	}
	if clock == nil {
		return nil, errors.New("ticket clock is required")
	}
	return &Service{repository: repository, idGenerator: idGenerator, clock: clock}, nil
}

// Confirm 确认工单草稿并原子创建持久化写操作 Job。
//
// 幂等：重复确认不会创建第二张工单，而是返回首次结果。这既是发布门槛要求的
// 安全属性，也是正常的用户行为——客户可能因为网络重试而重复提交确认。
func (service *Service) Confirm(
	ctx context.Context,
	customerID string,
	approvalID string,
) (Decision, error) {
	customerID, approvalID, err := normalizeScope(customerID, approvalID)
	if err != nil {
		return Decision{}, err
	}

	now := service.clock.Now().UTC()
	result, err := service.repository.ConfirmAndEnqueue(ctx, domain.ConfirmCommand{
		CustomerID:    customerID,
		ApprovalID:    approvalID,
		JobID:         service.idGenerator.NewID("job_"),
		TicketID:      service.idGenerator.NewID("tkt_"),
		TicketNumber:  service.ticketNumber(now),
		EventID:       service.idGenerator.NewID("evt_"),
		TicketEventID: service.idGenerator.NewID("evt_"),
		OccurredAt:    now,
	})
	if err != nil {
		return Decision{}, classifyApprovalError("confirm", err)
	}

	return Decision{Approval: result.Approval, Ticket: result.Ticket}, nil
}

// Cancel 取消工单草稿；取消后写操作永远不会执行。
func (service *Service) Cancel(
	ctx context.Context,
	customerID string,
	approvalID string,
) (Decision, error) {
	customerID, approvalID, err := normalizeScope(customerID, approvalID)
	if err != nil {
		return Decision{}, err
	}

	approval, err := service.repository.Cancel(
		ctx,
		customerID,
		approvalID,
		service.idGenerator.NewID("evt_"),
		service.clock.Now().UTC(),
	)
	if err != nil {
		return Decision{}, classifyApprovalError("cancel", err)
	}
	return Decision{Approval: approval}, nil
}

// Get 读取审批当前状态；Job 完成后同时返回稳定工单编号。
func (service *Service) Get(
	ctx context.Context,
	customerID string,
	approvalID string,
) (Decision, error) {
	approval, err := service.LoadApproval(ctx, customerID, approvalID)
	if err != nil {
		return Decision{}, err
	}
	decision := Decision{Approval: approval}
	if approval.TicketID == "" {
		return decision, nil
	}
	created, err := service.repository.LoadTicketByApproval(ctx, approval.ID)
	if err != nil {
		return Decision{}, classifyApprovalError("load ticket", err)
	}
	decision.Ticket = &created
	return decision, nil
}

// LoadApproval 读取客户授权范围内的审批，供界面回显草稿与当前状态。
func (service *Service) LoadApproval(
	ctx context.Context,
	customerID string,
	approvalID string,
) (domain.Approval, error) {
	customerID, approvalID, err := normalizeScope(customerID, approvalID)
	if err != nil {
		return domain.Approval{}, err
	}
	approval, err := service.repository.LoadApproval(ctx, customerID, approvalID)
	if err != nil {
		return domain.Approval{}, classifyApprovalError("load approval", err)
	}
	return approval, nil
}

// ticketNumber 生成人类可读的工单编号。
//
// 由时间和随机标识拼接，不依赖数据库序列：序列在幂等重试下会跳号，而编号会
// 展示给客户，跳号容易被误读为工单丢失。
func (service *Service) ticketNumber(now time.Time) string {
	suffix := strings.ToUpper(service.idGenerator.NewID(""))
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("TK-%s-%s", now.Format("20060102"), suffix)
}

func normalizeScope(customerID string, approvalID string) (string, string, error) {
	customerID = strings.TrimSpace(customerID)
	approvalID = strings.TrimSpace(approvalID)
	if customerID == "" || len(customerID) > maxScopedIDLength ||
		approvalID == "" || len(approvalID) > maxScopedIDLength {
		return "", "", newFailure("invalid_approval_scope", false, nil)
	}
	return customerID, approvalID, nil
}

// classifyApprovalError 把领域错误映射为稳定错误码。
//
// 不存在与不属于当前客户返回同一个码：区分两者会泄露其他客户是否持有该审批。
func classifyApprovalError(operation string, err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, domain.ErrNotFound):
		return newFailure("ticket_approval_not_found", false, err)
	case errors.Is(err, domain.ErrExpired):
		return newFailure("ticket_approval_expired", false, err)
	case errors.Is(err, domain.ErrInvalidState):
		return newFailure("ticket_approval_not_actionable", false, err)
	case errors.Is(err, domain.ErrInvalidCommand):
		return newFailure("invalid_ticket_command", false, err)
	default:
		return newFailure(operation+"_failed", true, err)
	}
}
